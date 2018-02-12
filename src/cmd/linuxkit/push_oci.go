package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	log "github.com/sirupsen/logrus"
)

func pushOci(args []string) {
	flags := flag.NewFlagSet("oci", flag.ExitOnError)
	invoked := filepath.Base(os.Args[0])
	flags.Usage = func() {
		fmt.Printf("USAGE: %s push oci [options] path\n\n", invoked)
		fmt.Printf("'path' is the full path to a OCI image. It will be uploaded to OCI Object Store and OCI VM image will be created from it.\n")
		fmt.Printf("Options:\n\n")
		flags.PrintDefaults()
	}
	namespaceFlag := flags.String("namespace", "", "Object store namespace. *Required*")
	bucketFlag := flags.String("bucket", "", "Object Store Bucket to upload to. *Required*")
	nameFlag := flags.String("img-name", "", "Overrides the name used to identify the file in Object Store. Defaults to the base of 'path' with the '.omi' suffix removed")

	if err := flags.Parse(args); err != nil {
		log.Fatal("Unable to parse args")
	}

	remArgs := flags.Args()
	if len(remArgs) == 0 {
		fmt.Printf("Please specify the path to the image to push\n")
		flags.Usage()
		os.Exit(1)
	}
	path := remArgs[0]

	bucket := getStringValue(bucketVar, *bucketFlag, "")
	namespace := getStringValue(objectStoreNamespaceEnVar, *namespaceFlag, "")
	name := getStringValue(nameVar, *nameFlag, "")

	if namespace == "" || bucket == "" {
		fmt.Printf("Namespace and bucket are required to push\n")
		flags.Usage()
		os.Exit(1)
	}

	const suffix = ".omi"
	if name == "" {
		name = strings.TrimSuffix(path, suffix)
		name = filepath.Base(name)
	}

	client, err := newOciClient()
	if err != nil {
		log.Fatalf("Unable to connect to OCI: %v", err)
	}

	err = client.UploadFile(path, namespace, bucket, name+suffix)
	if err != nil {
		log.Fatalf("Error copying to Oracle Object Storage: %v", err)
	}
	err = client.BuildImage(namespace, bucket, name+suffix)
	if err != nil {
		log.Fatalf("Error creating OCI Compute Image: %v", err)
	}
}
