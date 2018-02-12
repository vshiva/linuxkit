package main

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"path"
	"time"
)

const (
	vnicsURL            = "http://169.254.169.254/opc/v1/vnics/"
	instanceURL         = "http://169.254.169.254/opc/v1/instance/"
	instanceMetadataURL = "http://169.254.169.254/opc/v1/instance/metadata/"
	instanceHostNameURL = "http://169.254.169.254/openstack/latest/meta_data.json"
)

// OCIVNIC - Describes the network interface
type OCIVNIC struct {
	ID              string `json:"vnicId"`
	PrivateIP       string `json:"privateIp"`
	VirtualRouterIP string `json:"virtualRouterIp"`
	MacAddr         string `json:"macAddr"`
	SubnetCidrBlock string `json:"subnetCidrBlock"`
	VLanTag         int    `json:"vlanTag"`
}

//OCIInstance A OCI Instance Information
type OCIInstance struct {
	ID                 string `json:"id"`
	HostName           string `json:"hostname"`
	CompartmentID      string `json:"compartmentId"`
	Region             string `json:"region"`
	AvailabilityDomain string `json:"availabilityDomain"`
	Shape              string `json:"shape"`
}

// ProviderOCI is the type implementing the Provider interface for OCI
type ProviderOCI struct {
}

// NewOCI returns a new ProviderOCI
func NewOCI() *ProviderOCI {
	return &ProviderOCI{}
}

func (p *ProviderOCI) String() string {
	return "OCI"
}

// Probe checks if we are running on OCI
func (p *ProviderOCI) Probe() bool {
	// Getting the instaence metadata should always work...
	_, err := ociGet(instanceURL)
	return (err == nil)
}

// Extract gets both the OCI specific and generic userdata
func (p *ProviderOCI) Extract() ([]byte, error) {

	ociInstance := OCIInstance{}
	ociVincs := []OCIVNIC{}

	// Get Instance Metadata. This must not fail
	instanceData, err := ociGet(instanceURL)
	if err != nil {
		return nil, err
	}
	fmt.Printf("Instance Data : %s\n", instanceData)

	if err := json.Unmarshal(instanceData, &ociInstance); err != nil {
		return nil, err
	}

	// Load HostName
	hostNameData, err := ociGet(instanceHostNameURL)
	if err != nil {
		return nil, err
	}
	fmt.Printf("HostName Data : %s\n", hostNameData)

	if err := json.Unmarshal(hostNameData, &ociInstance); err != nil {
		return nil, err
	}

	// Load VNIC Info
	vnicData, err := ociGet(vnicsURL)
	if err != nil {
		return nil, err
	}
	fmt.Printf("VNIC Data : %s\n", vnicData)

	if err := json.Unmarshal(vnicData, &ociVincs); err != nil {
		return nil, err
	}

	if err := handleInstanceMetaData(ociInstance); err != nil {
		log.Printf("OCI: Failed to write host cloud provider metadata: %s", err)
	}

	if err := handleVNIC(ociVincs); err != nil {
		log.Printf("OCI: Failed to write vnic metadata: %s", err)
	}

	if err := handleSSH(); err != nil {
		log.Printf("OCI: Failed to get ssh data: %s", err)
	}

	// Generic userdata
	userData, err := ociGet(instanceMetadataURL + "userdata")
	if err != nil {
		log.Printf("OCI: Failed to get user-data: %s", err)
		// This is not an error
		return nil, nil
	}
	return userData, nil
}

// ociGet requests and extracts the requested URL
func ociGet(url string) ([]byte, error) {
	var client = &http.Client{
		Timeout: time.Second * 2,
	}

	req, err := http.NewRequest("", url, nil)
	if err != nil {
		return nil, fmt.Errorf("OCI: http.NewRequest failed: %s", err)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("OCI: Could not contact metadata service: %s", err)
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("OCI: Status not ok: %d", resp.StatusCode)
	}
	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("OCI: Failed to read http response: %s", err)
	}
	return body, nil
}

// write oci Metadata
func ociMetaWrite(metaDataName string, value string, fileMode os.FileMode) {

	if metaDataName != "" {
		// we got a value from the metadata server, now save to filesystem
		if err := ioutil.WriteFile(path.Join(ConfigPath, metaDataName), []byte(value), fileMode); err != nil {
			// we couldn't save the file
			log.Printf("OCI: Failed to write %s:%s %s", metaDataName, value, err)
		}
	} else {
		// metaDataName is empty. We did not get this value from the server
		log.Print("OCI: metaDataName is empty")
	}
}

// Host Cloud Provider Metadata
func handleInstanceMetaData(ociInstance OCIInstance) error {

	if err := ioutil.WriteFile(path.Join(ConfigPath, Hostname), []byte(ociInstance.HostName), 0644); err != nil {
		return err
	}

	ociMetaWrite("instance-id", ociInstance.ID, 0644)
	ociMetaWrite("compartment-id", ociInstance.CompartmentID, 0644)
	ociMetaWrite("region", ociInstance.Region, 0644)
	ociMetaWrite("availability-domain", ociInstance.AvailabilityDomain, 0644)
	ociMetaWrite("shape", ociInstance.Shape, 0644)

	return nil
}

// VNIC Metadata
func handleVNIC(vincs []OCIVNIC) error {
	for _, vnic := range vincs {
		vlan := fmt.Sprintf("vlan-%d", vnic.VLanTag)
		if err := os.Mkdir(path.Join(ConfigPath, vlan), 0755); err != nil {
			return fmt.Errorf("Failed to create %s: %s", SSH, err)
		}

		if err := ioutil.WriteFile(path.Join(ConfigPath, vlan, "vnic-id"), []byte(vnic.ID), 0644); err != nil {
			return fmt.Errorf("Failed to write vnic-id: %s", err)
		}

		if err := ioutil.WriteFile(path.Join(ConfigPath, vlan, "vnic-private-ip"), []byte(vnic.PrivateIP), 0644); err != nil {
			return fmt.Errorf("Failed to write vnic-private-ip: %s", err)
		}
		if err := ioutil.WriteFile(path.Join(ConfigPath, vlan, "vnic-mac-addr"), []byte(vnic.MacAddr), 0644); err != nil {
			return fmt.Errorf("Failed to write vnic-mac-addr: %s", err)
		}
		if err := ioutil.WriteFile(path.Join(ConfigPath, vlan, "virtual-rotuer-ip"), []byte(vnic.VirtualRouterIP), 0644); err != nil {
			return fmt.Errorf("Failed to write virtual-rotuer-ip: %s", err)
		}
		if err := ioutil.WriteFile(path.Join(ConfigPath, vlan, "subnet-cidr-block"), []byte(vnic.SubnetCidrBlock), 0644); err != nil {
			return fmt.Errorf("Failed to write subnet-cidr-block: %s", err)
		}
	}

	return nil
}

// SSH keys:
func handleSSH() error {
	sshKeys, err := ociGet(instanceMetadataURL + "ssh_authorized_keys")
	if err != nil {
		return fmt.Errorf("Failed to get sshKeys: %s", err)
	}

	if err := os.Mkdir(path.Join(ConfigPath, SSH), 0755); err != nil {
		return fmt.Errorf("Failed to create %s: %s", SSH, err)
	}

	err = ioutil.WriteFile(path.Join(ConfigPath, SSH, "authorized_keys"), sshKeys, 0600)
	if err != nil {
		return fmt.Errorf("Failed to write ssh keys: %s", err)
	}
	return nil
}
