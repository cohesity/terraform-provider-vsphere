package vsphere

import (
	"fmt"
	"log"

	"github.com/hashicorp/terraform/helper/schema"
	"github.com/hashicorp/terraform/helper/validation"
	"github.com/terraform-providers/terraform-provider-vsphere/vsphere/internal/helper/folder"
	"github.com/terraform-providers/terraform-provider-vsphere/vsphere/internal/helper/resourcepool"
	"github.com/terraform-providers/terraform-provider-vsphere/vsphere/internal/helper/virtualmachine"
	"github.com/terraform-providers/terraform-provider-vsphere/vsphere/internal/virtualdevice"
	"github.com/terraform-providers/terraform-provider-vsphere/vsphere/internal/vmworkflow"

	"github.com/vmware/govmomi/object"
)

func resourceCohesityHotStandbyVM() *schema.Resource {
	// The following keys are added in the schema as the internal code needs
	// those when computing the clone specs for the disks.
	// "disk" - this is computed from clone.0.moref_id
	// "scsi_controller_count" - this is computed from clone.0.moref_id
	s := map[string]*schema.Schema{
		"disk": {
			Type:        schema.TypeList,
			Optional:    true,
			Computed:    true,
			Description: "A specification for a virtual disk device on this virtual machine.",
			MaxItems:    60,
			Elem:        &schema.Resource{Schema: virtualdevice.DiskSubresourceSchema()},
		},
		"datastore_cluster_id": {
			Type:          schema.TypeString,
			Optional:      true,
			ConflictsWith: []string{"datastore_id"},
			Description:   "The ID of a datastore cluster to put the virtual machine in.",
		},
		"scsi_controller_count": {
			Type:         schema.TypeInt,
			Optional:     true,
			Default:      1,
			Description:  "The number of SCSI controllers that Terraform manages on this virtual machine. This directly affects the amount of disks you can add to the virtual machine and the maximum disk unit number. Note that lowering this value does not remove controllers.",
			ValidateFunc: validation.IntBetween(1, 4),
		},
		"resource_pool_id": {
			Type:        schema.TypeString,
			Required:    true,
			Description: "The ID of a resource pool to put the virtual machine in.",
		},
		"datastore_id": {
			Type:          schema.TypeString,
			Optional:      true,
			Computed:      true,
			ConflictsWith: []string{"datastore_cluster_id"},
			Description:   "The ID of the virtual machine's datastore. The virtual machine configuration is placed here, along with any virtual disks that are created without datastores.",
		},
		"folder": {
			Type:        schema.TypeString,
			Optional:    true,
			Description: "The name of the folder to locate the virtual machine in.",
			StateFunc:   folder.NormalizePath,
		},
		"name": {
			Type:        schema.TypeString,
			Optional:    true,
			Description: "The name of the cloned VM",
		},
		"moref_id": {
			Type:        schema.TypeString,
			Optional:    true,
			Description: "The moref_id of the virtual machine to power on.",
		},
		"default_ip_address": {
			Type:        schema.TypeString,
			Computed:    true,
			Description: "The IP address selected by Terraform to be used for the provisioner.",
		},
		"wait_for_guest_ip_timeout": {
			Type:        schema.TypeInt,
			Optional:    true,
			Default:     0,
			Description: "The amount of time, in minutes, to wait for an available IP address on this virtual machine. A value less than 1 disables the waiter.",
		},
		"wait_for_guest_net_timeout": {
			Type:        schema.TypeInt,
			Optional:    true,
			Default:     5,
			Description: "The amount of time, in minutes, to wait for an available IP address on this virtual machine. A value less than 1 disables the waiter.",
		},
		"wait_for_guest_net_routable": {
			Type:        schema.TypeBool,
			Optional:    true,
			Default:     true,
			Description: "Controls whether or not the guest network waiter waits for a routable address. When false, the waiter does not wait for a default gateway, nor are IP addresses checked against any discovered default gateways as part of its success criteria.",
		},
		"customize": {
			Type:        schema.TypeList,
			Optional:    true,
			MaxItems:    1,
			Description: "The customization spec for this virtual machine. This allows the user to configure the virtual machine after creation.",
			Elem:        &schema.Resource{Schema: vmworkflow.VirtualMachineCustomizeSchema()},
		},
		"ignored_guest_ips": {
			Type:        schema.TypeList,
			Optional:    true,
			Description: "List of IP addresses to ignore while waiting for an IP",
			Elem:        &schema.Schema{Type: schema.TypeString},
		},
		"clone": {
			Type:        schema.TypeList,
			Optional:    true,
			Description: "Clone details.",
			Elem: &schema.Resource{Schema: map[string]*schema.Schema{
				"moref_id": {
					Type:        schema.TypeString,
					Optional:    true,
					Description: "The moref_id of the virtual machine to power on.",
				},
				"timeout": {
					Type:        schema.TypeInt,
					Optional:    true,
					Default:     30,
					Description: "timeout for the clone operation",
				},
			},
			},
		},
	}
	return &schema.Resource{
		Create:        resourceCohesityHotStandbyVMCreate,
		Read:          resourceCohesityHotStandbyVMRead,
		Update:        resourceCohesityHotStandbyVMUpdate,
		Delete:        resourceCohesityHotStandbyVMDelete,
		SchemaVersion: 3,
		Schema:        s,
	}
}

func resourceCohesityHotStandbyVMCreate(d *schema.ResourceData, meta interface{}) error {
	var err error
	var vm *object.VirtualMachine
	var morefId string
	client := meta.(*VSphereClient).vimClient

	if len(d.Get("clone").([]interface{})) > 0 {
		log.Printf("[DEBUG] starting clone")
		vm, err = resourceCohesityHotStandbyClone(d, meta)
		morefId = "unknown"
		if err != nil {
			log.Printf("[DEBUG] failed during clone [%s]", err.Error())
			return err
		}
		log.Printf("[DEBUG] Clone was successfull.")
	} else {
		log.Print("[DEBUG] Starting creation of hot stand by resource.")
		if id, ok := d.GetOk("moref_id"); !ok {
			log.Printf("[DEBUG] moref not provided [%s]", id)
			d.SetId("")
			return nil
		} else {
			morefId = id.(string)
		}

		log.Printf("[DEBUG] Looking for vm with moref [%s]", morefId)
		vm, err = virtualmachine.FromMOID(client, morefId)
		if err != nil {
			if _, ok := err.(*virtualmachine.UUIDNotFoundError); ok {
				log.Printf("[DEBUG] Virtual machine not found, with moref: %s. Error: %s", morefId, err.Error())
				d.SetId("")
				return fmt.Errorf("Virtual machine not found, with moref %s.", morefId)
			}
			return fmt.Errorf("error searching for with moref %s: %s", morefId, err)
		}
	}
	vprops, err := virtualmachine.Properties(vm)
	if err != nil {
		d.SetId("")
		return nil
	}
	log.Printf("[DEBUG] VM %q - UUID is %q", vm.InventoryPath, vprops.Config.Uuid)
	d.SetId(vprops.Config.Uuid)

	var cw *virtualMachineCustomizationWaiter
	// Send customization spec if any has been defined.
	if len(d.Get("customize").([]interface{})) > 0 {
		if vprops.ResourcePool == nil {
			log.Printf("[DEBUG] [%s] Cannot find resource pool for the vm with moref %s", vprops.Config.Name, morefId)
			return fmt.Errorf("Cannot find resource pool for the vm [%s] moref [%s]", vprops.Config.Name, morefId)
		}

		poolID := vprops.ResourcePool.Value
		pool, err := resourcepool.FromID(client, poolID)
		if err != nil {
			return fmt.Errorf("could not find resource pool ID %q: %s", poolID, err)
		}
		// TODO(Mradul) guestId would be provided by magneto. Currently hardcoding for testing purpose.
		guestId := "centos7_64Guest"
		//family, err := resourcepool.OSFamily(client, pool, d.Get("guest_id").(string))
		family, err := resourcepool.OSFamily(client, pool, guestId)
		if err != nil {
			return fmt.Errorf("cannot find OS family for guest ID %q: %s", d.Get("guest_id").(string), err)
		}
		custSpec := vmworkflow.ExpandCustomizationSpec(d, family, "")
		cw = newVirtualMachineCustomizationWaiter(client, vm, d.Get("customize.0.timeout").(int))
		if err := virtualmachine.Customize(vm, custSpec); err != nil {
			// Roll back the VMs as per the error handling in reconfigure.
			if derr := resourceVSphereVirtualMachineDelete(d, meta); derr != nil {
				return fmt.Errorf(formatVirtualMachinePostCloneRollbackError, vm.InventoryPath, err, derr)
			}
			d.SetId("")
			return fmt.Errorf("error sending customization spec: %s", err)
		}
	}

	if err := virtualmachine.PowerOn(vm); err != nil {
		return fmt.Errorf("error powering on virtual machine: %s", err)
	}

	log.Printf("[DEBUG] Successfully powered on VM")

	// If we customized, wait on customization.
	if cw != nil {
		log.Printf("[DEBUG] %s: Waiting for VM customization to complete", resourceVSphereVirtualMachineIDString(d))
		<-cw.Done()
		if err := cw.Err(); err != nil {
			return fmt.Errorf(formatVirtualMachineCustomizationWaitError, vm.InventoryPath, err)
		}
	}

	// If user has provided static ip addresses, we will wait until the VM gets
	// that ip address. This is to avoid the case when for a brief period of time
	// the vm reports the old ip. After a few seconds the ip gets changed to the
	// user provided static ip.
	// TODO(Mradul): The problem is not solved for the DHCP case.
	ipv4Str := vmworkflow.GetCustomIPFromSpec(d, "")

	// Wait for guest IP address if we have been set to wait for one
	err = virtualmachine.WaitForGuestIP(
		client,
		vm,
		d.Get("wait_for_guest_ip_timeout").(int),
		d.Get("ignored_guest_ips").([]interface{}),
		ipv4Str,
	)
	if err != nil {
		return setErrorInResource(d, err)
	}

	// Wait for a routable address if we have been set to wait for one
	err = virtualmachine.WaitForGuestNet(
		client,
		vm,
		d.Get("wait_for_guest_net_routable").(bool),
		d.Get("wait_for_guest_net_timeout").(int),
		d.Get("ignored_guest_ips").([]interface{}),
		ipv4Str,
	)
	if err != nil {
		return setErrorInResource(d, err)
	}

	// All done!
	log.Printf("[DEBUG] %s: Create complete", resourceVSphereVirtualMachineIDString(d))
	return nil
}

func resourceCohesityHotStandbyVMRead(d *schema.ResourceData, meta interface{}) error {
	return nil
}

func resourceCohesityHotStandbyVMUpdate(d *schema.ResourceData, meta interface{}) error {
	return nil
}

func resourceCohesityHotStandbyVMDelete(d *schema.ResourceData, meta interface{}) error {
	client := meta.(*VSphereClient).vimClient
	id := d.Id()
	vm, err := virtualmachine.FromUUID(client, id)
	if err != nil {
		return fmt.Errorf("cannot locate virtual machine with UUID %q: %s", id, err)
	}

	timeout := 5
	if err := virtualmachine.GracefulPowerOff(client, vm, timeout, true); err != nil {
		return fmt.Errorf("error shutting down virtual machine: %s", err)
	}

	// If the VM was created as a result of the clone operation than delete it.
	if len(d.Get("clone").([]interface{})) > 0 {
		log.Printf("[DEBUG] This vm was created as part of clone. Deleting it.")
		if err := virtualmachine.Destroy(vm); err != nil {
			return fmt.Errorf("error destroying virtual machine: %s", err)
		}
	}

	d.SetId("")
	log.Printf("[DEBUG] %s: Delete complete", resourceVSphereVirtualMachineIDString(d))
	return nil
}

func resourceCohesityHotStandbyClone(d *schema.ResourceData, meta interface{}) (*object.VirtualMachine, error) {
	log.Printf("[DEBUG] %s: VM being created from clone", resourceVSphereVirtualMachineIDString(d))
	client := meta.(*VSphereClient).vimClient

	// Find the folder based off the path to the resource pool. Basically what we
	// are saying here is that the VM folder that we are placing this VM in needs
	// to be in the same hierarchy as the resource pool - so in other words, the
	// same datacenter.
	poolID := d.Get("resource_pool_id").(string)
	pool, err := resourcepool.FromID(client, poolID)
	if err != nil {
		return nil, fmt.Errorf("could not find resource pool ID %q: %s", poolID, err)
	}
	fo, err := folder.VirtualMachineFolderFromObject(client, pool, d.Get("folder").(string))
	if err != nil {
		return nil, err
	}

	// Expand the clone spec. We get the source VM here too.
	cloneSpec, srcVM, err := vmworkflow.ExpandCohesityVirtualMachineCloneSpec(d, client)
	if err != nil {
		return nil, err
	}

	// Start the clone
	name := d.Get("name").(string)
	timeout := d.Get("clone.0.timeout").(int)
	vm, err := virtualmachine.Clone(client, srcVM, fo, name, cloneSpec, timeout)
	if err != nil {
		return nil, fmt.Errorf("error cloning virtual machine: %s", err)
	}
	log.Printf("[DEBUG] clone completed. leaving function.")
	return vm, nil
}
