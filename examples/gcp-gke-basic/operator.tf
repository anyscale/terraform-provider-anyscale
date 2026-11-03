# ---------------------------------------------------------------------------------------------------------------------
# Anyscale Operator (opt-in: install_operator = true)
#   Installs the Anyscale Operator into the GKE cluster so the cloud can run workloads. The chart sets
#   the operator service account's Workload Identity annotation (iam.gke.io/gcp-service-account) from
#   global.auth.iamIdentity, matching the binding in anyscale_resources.tf. Routing uses the chart's
#   bundled ingress-nginx controller behind a LoadBalancer Service.
# ---------------------------------------------------------------------------------------------------------------------

provider "helm" {
  kubernetes {
    host                   = "https://${module.gke.endpoint}"
    token                  = data.google_client_config.default.access_token
    cluster_ca_certificate = base64decode(module.gke.ca_certificate)
  }
}

resource "helm_release" "anyscale_operator" {
  count = var.install_operator ? 1 : 0

  name             = "anyscale-operator"
  repository       = "https://anyscale.github.io/helm-charts"
  chart            = "anyscale-operator"
  version          = "1.12.0"
  namespace        = var.anyscale_k8s_namespace
  create_namespace = true

  values = [
    yamlencode({
      global = {
        # The helm key is "cloudDeploymentId", but it takes the cloud RESOURCE id (cldrsrc_...).
        cloudDeploymentId = anyscale_cloud.primary.cloud_resource_id
        cloudProvider     = "gcp"
        auth = {
          iamIdentity = google_service_account.gke_nodes.email
        }
        gcp = {
          projectId = var.google_project_id
        }
      }
      ingress-nginx = {
        enabled = true
      }
    })
  ]

  # The service account's project roles are listed so destroy uninstalls
  # the Operator before it loses them; referencing the account alone does
  # not order its role bindings.
  depends_on = [
    anyscale_cloud.primary,
    google_service_account_iam_binding.workload_identity_bindings,
    google_project_iam_member.gke_nodes_roles,
  ]
}
