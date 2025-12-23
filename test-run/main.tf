terraform {
  required_providers {
    anyscale = {
      source = "terraform-providers/anyscale"
    }
  }
}

provider "anyscale" {}

resource "anyscale_cloud" "test" {
  name            = "test-tf-logging"
  cloud_provider  = "AWS"
  region          = "us-east-2"
  compute_stack   = "VM"
  networking_mode = "PUBLIC"

  aws_config {
    vpc_id                = "vpc-0343edeee0eab27c3"
    subnet_ids            = ["subnet-086ac7bba68e3c1c3", "subnet-08a309019a027ec72"]
    security_group_ids    = ["sg-064dac0ed5cffc779"]
    s3_bucket_id          = "brent-tf-multisubnet-660e6d31fa4f"
    anyscale_iam_role_id  = "arn:aws:iam::367974485317:role/brent-tf-multisubnet-660e6d31fa4f-crossacct-iam-role"
    instance_iam_role_id  = "arn:aws:iam::367974485317:role/brent-tf-multisubnet-660e6d31fa4f-cluster-node-role"
    external_id           = "org_s73tw4mgxkgrtnw142gsvfmfaa-commonname-external-id-test"
  }
}
