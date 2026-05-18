package compute

import (
	"fmt"

	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
	"github.com/hashicorp/terraform-provider-google/google/registry"
	"github.com/hashicorp/terraform-provider-google/google/tpgresource"
	transport_tpg "github.com/hashicorp/terraform-provider-google/google/transport"
)

func DataSourceGoogleComputeInstance() *schema.Resource {
	// Generate datasource schema from resource
	dsSchema := tpgresource.DatasourceSchemaFromResourceSchema(ResourceComputeInstance().Schema)

	// Set 'Optional' schema elements
	tpgresource.AddOptionalFieldsToSchema(dsSchema, "name", "self_link", "project", "zone")

	return &schema.Resource{
		Read:   dataSourceGoogleComputeInstanceRead,
		Schema: dsSchema,
	}
}

func dataSourceGoogleComputeInstanceRead(d *schema.ResourceData, meta interface{}) error {
	config := meta.(*transport_tpg.Config)
	userAgent, err := tpgresource.GenerateUserAgentString(d, config.UserAgent)
	if err != nil {
		return err
	}

	project, zone, name, err := tpgresource.GetZonalResourcePropertiesFromSelfLinkOrSchema(d, config)
	if err != nil {
		return err
	}

	id := fmt.Sprintf("projects/%s/zones/%s/instances/%s", project, zone, name)

	url := fmt.Sprintf("%sprojects/%s/zones/%s/instances/%s",
		transport_tpg.BaseUrl(Product, config), project, zone, name)
	instance, err := transport_tpg.SendRequest(transport_tpg.SendRequestOptions{
		Config:    config,
		Method:    "GET",
		Project:   project,
		RawURL:    url,
		UserAgent: userAgent,
	})
	if err != nil {
		return transport_tpg.HandleDataSourceNotFoundError(err, d, fmt.Sprintf("Instance %s", name), id)
	}

	// Flatten metadata
	md := map[string]string{}
	if metaRaw, ok := instance["metadata"].(map[string]interface{}); ok && metaRaw != nil {
		if items, ok := metaRaw["items"].([]interface{}); ok {
			for _, item := range items {
				entry, _ := item.(map[string]interface{})
				key, _ := entry["key"].(string)
				value, _ := entry["value"].(string)
				md[key] = value
			}
		}
		if fingerprint, ok := metaRaw["fingerprint"].(string); ok {
			if err := d.Set("metadata_fingerprint", fingerprint); err != nil {
				return fmt.Errorf("Error setting metadata_fingerprint: %s", err)
			}
		}
	}
	if err = d.Set("metadata", md); err != nil {
		return fmt.Errorf("error setting metadata: %s", err)
	}

	canIpForward, _ := instance["canIpForward"].(bool)
	if err := d.Set("can_ip_forward", canIpForward); err != nil {
		return fmt.Errorf("Error setting can_ip_forward: %s", err)
	}
	machineType, _ := instance["machineType"].(string)
	if err := d.Set("machine_type", tpgresource.GetResourceNameFromSelfLink(machineType)); err != nil {
		return fmt.Errorf("Error setting machine_type: %s", err)
	}
	hostname, _ := instance["hostname"].(string)
	if err := d.Set("hostname", hostname); err != nil {
		return fmt.Errorf("Error setting hostname: %s", err)
	}

	// Set the networks
	// Use the first external IP found for the default connection info.
	networkInterfacesRaw, _ := instance["networkInterfaces"].([]interface{})
	networkInterfaces, _, internalIP, externalIP, err := flattenNetworkInterfaces(d, config, networkInterfacesRaw)
	if err != nil {
		return err
	}
	if err := d.Set("network_interface", networkInterfaces); err != nil {
		return err
	}

	// Fall back on internal ip if there is no external ip.  This makes sense in the situation where
	// terraform is being used on a cloud instance and can therefore access the instances it creates
	// via their internal ips.
	sshIP := externalIP
	if sshIP == "" {
		sshIP = internalIP
	}

	// Initialize the connection info
	d.SetConnInfo(map[string]string{
		"type": "ssh",
		"host": sshIP,
	})

	// Set the tags fingerprint if there is one.
	if tags, ok := instance["tags"].(map[string]interface{}); ok && tags != nil {
		if fingerprint, ok := tags["fingerprint"].(string); ok {
			if err := d.Set("tags_fingerprint", fingerprint); err != nil {
				return fmt.Errorf("Error setting tags_fingerprint: %s", err)
			}
		}
		if items, ok := tags["items"].([]interface{}); ok {
			tagStrings := make([]string, 0, len(items))
			for _, item := range items {
				if s, ok := item.(string); ok {
					tagStrings = append(tagStrings, s)
				}
			}
			if err := d.Set("tags", tpgresource.ConvertStringArrToInterface(tagStrings)); err != nil {
				return fmt.Errorf("Error setting tags: %s", err)
			}
		}
	}

	instanceLabels, _ := instance["labels"].(map[string]interface{})
	if err := d.Set("labels", instanceLabels); err != nil {
		return err
	}

	if err := d.Set("terraform_labels", instanceLabels); err != nil {
		return err
	}

	if labelFingerprint, ok := instance["labelFingerprint"].(string); ok && labelFingerprint != "" {
		if err := d.Set("label_fingerprint", labelFingerprint); err != nil {
			return fmt.Errorf("Error setting label_fingerprint: %s", err)
		}
	}

	attachedDisks := []map[string]interface{}{}
	scratchDisks := []map[string]interface{}{}
	disksRaw, _ := instance["disks"].([]interface{})
	for _, rawDisk := range disksRaw {
		disk, _ := rawDisk.(map[string]interface{})
		if disk == nil {
			continue
		}
		isBoot, _ := disk["boot"].(bool)
		diskType, _ := disk["type"].(string)
		diskSource, _ := disk["source"].(string)
		diskDeviceName, _ := disk["deviceName"].(string)
		diskMode, _ := disk["mode"].(string)

		if isBoot {
			if err = d.Set("boot_disk", flattenBootDisk(d, disk, config)); err != nil {
				return err
			}
		} else if diskType == "SCRATCH" {
			scratchDisks = append(scratchDisks, flattenScratchDisk(disk))
		} else {
			di := map[string]interface{}{
				"source":      tpgresource.ConvertSelfLinkToV1(diskSource),
				"device_name": diskDeviceName,
				"mode":        diskMode,
			}
			if key, ok := disk["diskEncryptionKey"].(map[string]interface{}); ok && key != nil {
				if sha256, _ := key["sha256"].(string); sha256 != "" {
					di["disk_encryption_key_sha256"] = sha256
				}
				if kmsKeyName, _ := key["kmsKeyName"].(string); kmsKeyName != "" {
					di["kms_key_self_link"] = kmsKeyName
				}
			}
			attachedDisks = append(attachedDisks, di)
		}
	}
	// Remove nils from map in case there were disks in the config that were not present on read;
	// i.e. a disk was detached out of band
	ads := []map[string]interface{}{}
	for _, d := range attachedDisks {
		if d != nil {
			ads = append(ads, d)
		}
	}

	serviceAccountsRaw, _ := instance["serviceAccounts"].([]interface{})
	err = d.Set("service_account", flattenServiceAccounts(serviceAccountsRaw))
	if err != nil {
		return err
	}

	schedulingRaw, _ := instance["scheduling"].(map[string]interface{})
	err = d.Set("scheduling", flattenScheduling(schedulingRaw))
	if err != nil {
		return err
	}

	guestAcceleratorsRaw, _ := instance["guestAccelerators"].([]interface{})
	err = d.Set("guest_accelerator", flattenGuestAccelerators(guestAcceleratorsRaw))
	if err != nil {
		return err
	}

	err = d.Set("scratch_disk", scratchDisks)
	if err != nil {
		return err
	}

	shieldedInstanceConfigRaw, _ := instance["shieldedInstanceConfig"].(map[string]interface{})
	err = d.Set("shielded_instance_config", flattenShieldedVmConfig(shieldedInstanceConfigRaw))
	if err != nil {
		return err
	}

	displayDeviceRaw, _ := instance["displayDevice"].(map[string]interface{})
	err = d.Set("enable_display", flattenEnableDisplay(displayDeviceRaw))
	if err != nil {
		return err
	}

	if err := d.Set("attached_disk", ads); err != nil {
		return fmt.Errorf("Error setting attached_disk: %s", err)
	}
	cpuPlatform, _ := instance["cpuPlatform"].(string)
	if err := d.Set("cpu_platform", cpuPlatform); err != nil {
		return fmt.Errorf("Error setting cpu_platform: %s", err)
	}
	minCpuPlatform, _ := instance["minCpuPlatform"].(string)
	if err := d.Set("min_cpu_platform", minCpuPlatform); err != nil {
		return fmt.Errorf("Error setting min_cpu_platform: %s", err)
	}
	deletionProtection, _ := instance["deletionProtection"].(bool)
	if err := d.Set("deletion_protection", deletionProtection); err != nil {
		return fmt.Errorf("Error setting deletion_protection: %s", err)
	}
	selfLink, _ := instance["selfLink"].(string)
	if err := d.Set("self_link", tpgresource.ConvertSelfLinkToV1(selfLink)); err != nil {
		return fmt.Errorf("Error setting self_link: %s", err)
	}
	var instanceId int64
	if idRaw, ok := instance["id"].(float64); ok {
		instanceId = int64(idRaw)
	}
	if err := d.Set("instance_id", fmt.Sprintf("%d", instanceId)); err != nil {
		return fmt.Errorf("Error setting instance_id: %s", err)
	}
	if err := d.Set("project", project); err != nil {
		return fmt.Errorf("Error setting project: %s", err)
	}
	instanceZone, _ := instance["zone"].(string)
	if err := d.Set("zone", tpgresource.GetResourceNameFromSelfLink(instanceZone)); err != nil {
		return fmt.Errorf("Error setting zone: %s", err)
	}
	instanceStatus, _ := instance["status"].(string)
	if err := d.Set("current_status", instanceStatus); err != nil {
		return fmt.Errorf("Error setting current_status: %s", err)
	}
	instanceName, _ := instance["name"].(string)
	if err := d.Set("name", instanceName); err != nil {
		return fmt.Errorf("Error setting name: %s", err)
	}
	keyRevocationActionType, _ := instance["keyRevocationActionType"].(string)
	if err := d.Set("key_revocation_action_type", keyRevocationActionType); err != nil {
		return fmt.Errorf("Error setting key_revocation_action_type: %s", err)
	}
	creationTimestamp, _ := instance["creationTimestamp"].(string)
	if err := d.Set("creation_timestamp", creationTimestamp); err != nil {
		return fmt.Errorf("Error setting creation_timestamp: %s", err)
	}

	d.SetId(fmt.Sprintf("projects/%s/zones/%s/instances/%s", project, tpgresource.GetResourceNameFromSelfLink(instanceZone), instanceName))
	return nil
}

func init() {
	registry.Schema{
		Name:        "google_compute_instance",
		ProductName: "compute",
		Type:        registry.SchemaTypeDataSource,
		Schema:      DataSourceGoogleComputeInstance(),
	}.Register()
}
