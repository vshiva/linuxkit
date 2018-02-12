package main

import (
	"bytes"
	"context"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"sync"

	log "github.com/sirupsen/logrus"

	"github.com/oracle/oci-go-sdk/core"

	"github.com/oracle/oci-go-sdk/common"
	obstore "github.com/oracle/oci-go-sdk/objectstorage"
)

const (
	maxPartSize               = 32768 * 2
	maxConcurrency            = 20
	contentType               = "application/octet-stream"
	objectStoreNamespaceEnVar = "OCI_OBJECSTORE_NAMESPACE"
)

type (
	chunk struct {
		num  int
		data []byte
		etag string
		try  int
	}

	ociClient struct {
		obstoreClient obstore.ObjectStorageClient
		computeClient core.ComputeClient
	}
)

var (
	errorc   = make(chan error, 5)
	chunks   = make(chan chunk, 1000)
	success  = make(chan chunk, 1000)
	progress = make(chan int, 100)
)

// strPtr returns a pointer to the string value passed in
func strPtr(s string) *string {
	return &s
}

// intPtr returns a pointer to the int value passed in.
func intPtr(v int) *int {
	return &v
}

func (o ociClient) LaunchInstance() {

}

func (o ociClient) UploadFile(fileLoc, namespace, bucketName, nameWithSuffix string) error {

	mp, err := o.obstoreClient.CreateMultipartUpload(context.Background(), obstore.CreateMultipartUploadRequest{
		NamespaceName: &namespace,
		BucketName:    &bucketName,
		CreateMultipartUploadDetails: obstore.CreateMultipartUploadDetails{
			Object:      &nameWithSuffix,
			ContentType: strPtr(contentType),
		},
	})

	if err != nil {
		log.Error("Failed to create multi part upload")
		return err
	}

	file, err := os.Open(filepath.Join(fileLoc, nameWithSuffix))
	if err != nil {
		log.Errorf("err opening file: %s", err)
		return err
	}
	defer file.Close()

	fileInfo, _ := file.Stat()
	size := fileInfo.Size()

	numChunks := int(size/maxPartSize) + 1
	concurrency := numChunks
	if concurrency > maxConcurrency {
		concurrency = maxConcurrency
	}

	wg := new(sync.WaitGroup)
	wg.Add(concurrency)

	for i := 0; i < concurrency; i++ {
		// parallel routine for handling chunks
		go handleChunks(o.obstoreClient, mp, wg)
	}

	go readInputFile(file)
	go showProgress(numChunks)

	// Waiting for all the concurrent uploads
	go func() {
		wg.Wait()
		close(success)
		close(progress)
	}()

	handleComplete(o.obstoreClient, mp)
	close(errorc)

	select {
	case e := <-errorc:
		if e != nil {
			log.Errorf("Aborting... %v\n", e)
			abortMultipartUpload(o.obstoreClient, mp)
		}
	}

	return nil
}

func (o ociClient) BuildImage(namespace, bucketName, name string) error {

	return nil
}

func newOciClient() (*ociClient, error) {

	obStoreClient, err := obstore.NewObjectStorageClientWithConfigurationProvider(common.DefaultConfigProvider())
	if err != nil {
		log.Errorf("Error Creating Object Store Client - %v\n", err)
		return nil, err
	}

	computeClient, err := core.NewComputeClientWithConfigurationProvider(common.DefaultConfigProvider())
	if err != nil {
		log.Errorf("Error Creating Object Store Client - %v\n", err)
		return nil, err
	}

	return &ociClient{
		obstoreClient: obStoreClient,
		computeClient: computeClient,
	}, nil
}

func readInputFile(file *os.File) {

	fileInfo, _ := file.Stat()
	size := fileInfo.Size()
	buffer := make([]byte, size)
	file.Read(buffer)
	var curr, partLength int64
	var remaining = size
	partNumber := 1

	for curr = 0; remaining != 0; curr += partLength {
		if remaining < maxPartSize {
			partLength = remaining
		} else {
			partLength = maxPartSize
		}
		chunks <- chunk{num: partNumber, data: buffer[curr : curr+partLength]}
		remaining -= partLength
		partNumber++
	}
	close(chunks)
}

func uploadPart(client obstore.ObjectStorageClient, mp obstore.CreateMultipartUploadResponse, chk chunk) (*chunk, error) {

	partInput := obstore.UploadPartRequest{
		NamespaceName:  mp.Namespace,
		BucketName:     mp.Bucket,
		ObjectName:     mp.Object,
		UploadId:       mp.UploadId,
		UploadPartBody: ioutil.NopCloser(bytes.NewReader(chk.data)),
		ContentLength:  intPtr(len(chk.data)),
		UploadPartNum:  intPtr(chk.num),
	}
	resp, err := client.UploadPart(context.Background(), partInput)
	if err != nil {
		return nil, err
	}
	progress <- 1

	return &chunk{
		etag: *resp.ETag,
		num:  chk.num,
	}, nil
}

func abortMultipartUpload(client obstore.ObjectStorageClient, resp obstore.CreateMultipartUploadResponse) (obstore.AbortMultipartUploadResponse, error) {
	log.Errorf("Aborting multipart upload. # %v\t%v\t%v\t%v\n", *resp.UploadId, *resp.Namespace, *resp.Bucket, *resp.Object)
	abortInput := obstore.AbortMultipartUploadRequest{
		NamespaceName: resp.Namespace,
		BucketName:    resp.Bucket,
		UploadId:      resp.UploadId,
	}

	return client.AbortMultipartUpload(context.Background(), abortInput)
}

func handleChunks(client obstore.ObjectStorageClient, mp obstore.CreateMultipartUploadResponse, wg *sync.WaitGroup) {
	defer wg.Done()

	for chk := range chunks {
		chk.try++
		schunk, err := uploadPart(client, mp, chk)
		if err != nil {
			log.Errorf("Error uploading %v\n", err)
			errorc <- err
			return
		}
		success <- *schunk
	}
}

func handleComplete(client obstore.ObjectStorageClient, mp obstore.CreateMultipartUploadResponse) {

	parts := make([]obstore.CommitMultipartUploadPartDetails, 0)
	for chk := range success {
		parts = append(parts, obstore.CommitMultipartUploadPartDetails{
			PartNum: intPtr(chk.num),
			Etag:    strPtr(chk.etag),
		})
	}

	commitRequest := obstore.CommitMultipartUploadRequest{
		BucketName:    mp.Bucket,
		NamespaceName: mp.Namespace,
		ObjectName:    mp.Object,
		UploadId:      mp.UploadId,
		CommitMultipartUploadDetails: obstore.CommitMultipartUploadDetails{
			PartsToCommit: parts,
		},
	}

	if _, err := client.CommitMultipartUpload(context.Background(), commitRequest); err != nil {
		errorc <- err
		return
	}

	fmt.Println("\rUpload Done.                    ")
}

func showProgress(numChunks int) {
	p := 1
	for range progress {
		pct := float64(p) / float64(numChunks)
		fmt.Printf("\rUploading... %d%%", int(pct*100))
		p++
	}
}
