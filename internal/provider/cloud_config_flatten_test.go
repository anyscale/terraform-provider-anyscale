package provider

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"
)

func TestResolveIsEmptyCloud(t *testing.T) {
	tests := []struct {
		name          string
		current       types.Bool
		resourceCount int
		want          bool
	}{
		{"null with zero resources resolves true", types.BoolNull(), 0, true},
		{"null with resources resolves false", types.BoolNull(), 2, false},
		{"unknown with zero resources resolves true", types.BoolUnknown(), 0, true},
		{"sticky true is never re-derived even with resources now present", types.BoolValue(true), 3, true},
		{"sticky false is never re-derived even with zero resources", types.BoolValue(false), 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolveIsEmptyCloud(tt.current, tt.resourceCount)
			if got.ValueBool() != tt.want {
				t.Errorf("resolveIsEmptyCloud() = %v, want %v", got.ValueBool(), tt.want)
			}
		})
	}
}

func TestFlattenAWSConfig_PrefersSubnetIDsToAZOverSubnetIDs(t *testing.T) {
	ctx := context.Background()
	cfg := &AWSConfig{
		VPCID:             "vpc-123",
		SubnetIDs:         []string{"subnet-a", "subnet-b"},
		Zones:             []string{"us-east-2a", "us-east-2b"},
		SecurityGroupIDs:  []string{"sg-1"},
		AnyscaleIAMRoleID: "arn:aws:iam::123:role/control",
		ClusterIAMRoleID:  "arn:aws:iam::123:role/data",
		ExternalID:        "ext-id",
	}

	obj, diags := flattenAWSConfig(ctx, cfg)
	if diags.HasError() {
		t.Fatalf("unexpected error: %v", diags)
	}

	var model AWSConfigModel
	diags = obj.As(ctx, &model, basetypes.ObjectAsOptions{})
	if diags.HasError() {
		t.Fatalf("unexpected error converting back: %v", diags)
	}

	// Hazard: must populate subnet_ids_to_az (the lossless, preferred form),
	// and leave subnet_ids null - populating both, or populating the plain
	// list instead, would risk a diff against whichever shape the user's
	// real config actually uses.
	if !model.SubnetIDs.IsNull() {
		t.Errorf("SubnetIDs = %v, want null (subnet_ids_to_az is populated instead)", model.SubnetIDs)
	}
	if model.SubnetIDsToAZ.IsNull() {
		t.Fatal("SubnetIDsToAZ is null, want populated from parallel subnet_ids/zones")
	}
	azMap := make(map[string]string)
	model.SubnetIDsToAZ.ElementsAs(ctx, &azMap, false)
	if azMap["subnet-a"] != "us-east-2a" || azMap["subnet-b"] != "us-east-2b" {
		t.Errorf("SubnetIDsToAZ = %v, want {subnet-a: us-east-2a, subnet-b: us-east-2b}", azMap)
	}

	if model.VPCID.ValueString() != "vpc-123" {
		t.Errorf("VPCID = %v, want vpc-123", model.VPCID.ValueString())
	}
	if model.ControlplaneIAMRoleARN.ValueString() != "arn:aws:iam::123:role/control" {
		t.Errorf("ControlplaneIAMRoleARN = %v, want the control-plane ARN", model.ControlplaneIAMRoleARN.ValueString())
	}
}

func TestFlattenAWSConfig_NilReturnsNullObject(t *testing.T) {
	obj, diags := flattenAWSConfig(context.Background(), nil)
	if diags.HasError() {
		t.Fatalf("unexpected error: %v", diags)
	}
	if !obj.IsNull() {
		t.Error("expected null object for nil AWSConfig")
	}
}

func TestStripBucketPrefix(t *testing.T) {
	tests := []struct {
		name       string
		provider   string
		bucketName string
		want       string
	}{
		{"AWS strips s3:// to match documented bare convention", "AWS", "s3://my-bucket", "my-bucket"},
		{"AWS bucket with no prefix is unchanged", "AWS", "my-bucket", "my-bucket"},
		{"lowercase aws still strips", "aws", "s3://my-bucket", "my-bucket"},
		{"GCP keeps gs:// to match documented prefixed convention", "GCP", "gs://my-bucket", "gs://my-bucket"},
		{"Azure keeps abfss:// verbatim - never prepended, never stripped", "AZURE", "abfss://container@account.dfs.core.windows.net", "abfss://container@account.dfs.core.windows.net"},
		{"lowercase azure still passes through unchanged", "azure", "abfss://container@account.dfs.core.windows.net", "abfss://container@account.dfs.core.windows.net"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := stripBucketPrefix(tt.provider, tt.bucketName)
			if got != tt.want {
				t.Errorf("stripBucketPrefix(%q, %q) = %q, want %q", tt.provider, tt.bucketName, got, tt.want)
			}
		})
	}
}

func TestFlattenObjectStorage_ProviderAwarePrefix(t *testing.T) {
	ctx := context.Background()

	t.Run("AWS: API's prefixed value flattens to bare, matching the documented convention", func(t *testing.T) {
		region := "us-east-1"
		obj, diags := flattenObjectStorage(&ObjectStorage{BucketName: "s3://my-bucket", Region: &region}, "AWS")
		if diags.HasError() {
			t.Fatalf("unexpected error: %v", diags)
		}
		var model ObjectStorageModel
		obj.As(ctx, &model, basetypes.ObjectAsOptions{})
		if model.BucketName.ValueString() != "my-bucket" {
			t.Errorf("BucketName = %q, want bare \"my-bucket\"", model.BucketName.ValueString())
		}
	})

	t.Run("GCP: API's prefixed value is left as-is, matching the documented convention", func(t *testing.T) {
		obj, diags := flattenObjectStorage(&ObjectStorage{BucketName: "gs://my-bucket"}, "GCP")
		if diags.HasError() {
			t.Fatalf("unexpected error: %v", diags)
		}
		var model ObjectStorageModel
		obj.As(ctx, &model, basetypes.ObjectAsOptions{})
		if model.BucketName.ValueString() != "gs://my-bucket" {
			t.Errorf("BucketName = %q, want prefixed \"gs://my-bucket\"", model.BucketName.ValueString())
		}
	})
}

func TestFlattenKubernetesConfig_OnlyAPIBackedFieldsPopulateNamespaceGetsDefault(t *testing.T) {
	ctx := context.Background()
	cfg := &KubernetesConfig{
		AnyscaleOperatorIAMIdentity: "arn:aws:iam::123:role/operator",
		Zones:                       []string{"us-east-2a"},
		RedisEndpoint:               "redis.ray-system.svc.cluster.local:6379",
	}

	obj, diags := flattenKubernetesConfig(ctx, cfg)
	if diags.HasError() {
		t.Fatalf("unexpected error: %v", diags)
	}

	var model KubernetesConfigModel
	obj.As(ctx, &model, basetypes.ObjectAsOptions{})

	if model.AnyscaleOperatorIAMIdentity.ValueString() != "arn:aws:iam::123:role/operator" {
		t.Errorf("AnyscaleOperatorIAMIdentity = %v, want the operator ARN", model.AnyscaleOperatorIAMIdentity.ValueString())
	}
	// namespace has no API-backed source of truth - it must get the schema's
	// own documented default, not null, to match a config that never set it.
	if model.Namespace.ValueString() != "anyscale" {
		t.Errorf("Namespace = %v, want \"anyscale\" (the schema default)", model.Namespace.ValueString())
	}
	// The other four inert fields (C5) have no API-backed value at all - they
	// must be null, not fabricated.
	for name, got := range map[string]types.String{
		"IngressHost":    model.IngressHost,
		"ClusterName":    model.ClusterName,
		"Context":        model.Context,
		"KubeconfigPath": model.KubeconfigPath,
	} {
		if !got.IsNull() {
			t.Errorf("%s = %v, want null (not sent to or returned by the API)", name, got)
		}
	}
}

func TestFlattenFileStorage_MountPathFallsBackOnlyWhenAPIOmitsIt(t *testing.T) {
	ctx := context.Background()

	t.Run("API-provided mount_path is preserved verbatim", func(t *testing.T) {
		obj, diags := flattenFileStorage(ctx, &FileStorage{FileStorageID: "fs-1", MountPath: "/custom/path"})
		if diags.HasError() {
			t.Fatalf("unexpected error: %v", diags)
		}
		var model FileStorageModel
		obj.As(ctx, &model, basetypes.ObjectAsOptions{})
		if model.MountPath.ValueString() != "/custom/path" {
			t.Errorf("MountPath = %v, want /custom/path (must not be overwritten by the default)", model.MountPath.ValueString())
		}
	})

	t.Run("empty API mount_path falls back to the schema default", func(t *testing.T) {
		obj, diags := flattenFileStorage(ctx, &FileStorage{FileStorageID: "fs-1"})
		if diags.HasError() {
			t.Fatalf("unexpected error: %v", diags)
		}
		var model FileStorageModel
		obj.As(ctx, &model, basetypes.ObjectAsOptions{})
		if model.MountPath.ValueString() != "/mnt/shared" {
			t.Errorf("MountPath = %v, want /mnt/shared (schema default)", model.MountPath.ValueString())
		}
	})
}

// TestFlattenAWSConfig_ClusterInstanceProfileID is a regression test for C6:
// aws_config.cluster_instance_profile_id must round-trip through import like
// every other optional AWSConfig field.
func TestFlattenAWSConfig_ClusterInstanceProfileID(t *testing.T) {
	ctx := context.Background()

	t.Run("set value is preserved", func(t *testing.T) {
		profileID := "arn:aws:iam::123:instance-profile/custom"
		obj, diags := flattenAWSConfig(ctx, &AWSConfig{ClusterInstanceProfileID: &profileID})
		if diags.HasError() {
			t.Fatalf("unexpected error: %v", diags)
		}
		var model AWSConfigModel
		obj.As(ctx, &model, basetypes.ObjectAsOptions{})
		if model.ClusterInstanceProfileID.ValueString() != profileID {
			t.Errorf("ClusterInstanceProfileID = %v, want %v", model.ClusterInstanceProfileID.ValueString(), profileID)
		}
	})

	t.Run("unset (nil) API value flattens to null, not the API-side default", func(t *testing.T) {
		obj, diags := flattenAWSConfig(ctx, &AWSConfig{})
		if diags.HasError() {
			t.Fatalf("unexpected error: %v", diags)
		}
		var model AWSConfigModel
		obj.As(ctx, &model, basetypes.ObjectAsOptions{})
		if !model.ClusterInstanceProfileID.IsNull() {
			t.Errorf("ClusterInstanceProfileID = %v, want null", model.ClusterInstanceProfileID)
		}
	})
}

// TestFlattenFileStorage_PVCAndCSIDriver is a regression test for C6:
// file_storage.persistent_volume_claim and .csi_ephemeral_volume_driver must
// round-trip like every other optional FileStorage field.
func TestFlattenFileStorage_PVCAndCSIDriver(t *testing.T) {
	ctx := context.Background()
	obj, diags := flattenFileStorage(ctx, &FileStorage{
		FileStorageID:            "fs-1",
		PersistentVolumeClaim:    "ray-shared-pvc",
		CSIEphemeralVolumeDriver: "ephemeral.csi.example.com",
	})
	if diags.HasError() {
		t.Fatalf("unexpected error: %v", diags)
	}
	var model FileStorageModel
	obj.As(ctx, &model, basetypes.ObjectAsOptions{})
	if model.PersistentVolumeClaim.ValueString() != "ray-shared-pvc" {
		t.Errorf("PersistentVolumeClaim = %v, want ray-shared-pvc", model.PersistentVolumeClaim.ValueString())
	}
	if model.CSIEphemeralVolumeDriver.ValueString() != "ephemeral.csi.example.com" {
		t.Errorf("CSIEphemeralVolumeDriver = %v, want ephemeral.csi.example.com", model.CSIEphemeralVolumeDriver.ValueString())
	}
}
