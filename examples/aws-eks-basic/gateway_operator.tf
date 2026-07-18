# ---------------------------------------------------------------------------------------------------------------------
# Envoy Gateway + Anyscale Operator install, fully native Terraform (no scripts, no manually-run kubectl).
#
# THE CORE PROBLEM: kubernetes_manifest does a server-side dry-run AT PLAN TIME to compute its plan, which requires
# the target CRD already registered in the live cluster. depends_on only orders APPLY, never PLAN - so a from-scratch
# single `terraform apply` fails at the plan step for any kubernetes_manifest whose CRD is installed by another
# resource in the SAME apply, full stop. Confirmed against real hashicorp/terraform-provider-kubernetes issues
# #2597/#2334/#1367 and HashiCorp's own maintainers, who track this as an open "progressive apply" gap, not a bug
# with a clever HCL fix.
#
# FIX: split into two phases behind var.install_gateway_resources (see variables.tf). Phase 1 (always applied)
# installs Envoy Gateway via helm (CRDs + controller together; helm_release has no plan-time GVR requirement).
# Phase 2 (var.install_gateway_resources = true, applied on a SECOND `terraform apply` once phase 1 has actually
# run) creates the Gateway API objects via kubernetes_manifest - by then the CRDs already exist, so the plan-time
# dry-run succeeds normally, and the wait{} block is safe to use. Every count = var.install_gateway_resources ? 1 : 0
# below is the load-bearing mechanism: at count = 0, Terraform never evaluates that resource against the live
# cluster at all (no schema resolution, no dry-run), so the CRD-not-registered-yet trap cannot fire on the first
# apply. See README.md for the full two-apply walkthrough.
# ---------------------------------------------------------------------------------------------------------------------

# ---------------------------------------------------------------------------------------------------------------------
# PHASE 1 (always applied): Envoy Gateway CRDs + controller.
# ---------------------------------------------------------------------------------------------------------------------

locals {
  # Known limitation (see README.md's "Known limitation: Envoy Gateway chart pull" section for the
  # full why): the Terraform helm provider (v2.17.0) cannot pull this chart directly from its OCI
  # registry - "insufficient_scope: authorization failed" - even though the standalone helm CLI
  # pulls the identical public chart fine on the same host at the same time. This is a genuine gap
  # in the provider's own OCI client (matches the documented class of issue in
  # hashicorp/terraform-provider-helm#1397), not a registry/auth config problem this example can
  # route around with a `registry` block - there is nothing to authenticate for an anonymous public
  # pull. Required one-time prerequisite instead of a silent workaround:
  #   helm pull oci://docker.io/envoyproxy/gateway-helm --version 1.8.2 -d ${path.module}/.charts
  envoy_gateway_chart_version = "1.8.2"
  envoy_gateway_chart_path    = "${path.module}/.charts/gateway-helm-v${local.envoy_gateway_chart_version}.tgz"
}

resource "helm_release" "envoy_gateway" {
  name = "eg"

  chart   = local.envoy_gateway_chart_path
  version = local.envoy_gateway_chart_version

  namespace        = "envoy-gateway-system"
  create_namespace = true

  # Ordering landmine: nodes must be Ready (vpc-cni + pod-identity-agent
  # installed before_compute) before any helm install can schedule pods.
  depends_on = [module.eks]

  lifecycle {
    precondition {
      condition     = fileexists(local.envoy_gateway_chart_path)
      error_message = "Envoy Gateway chart not found at ${local.envoy_gateway_chart_path}. This is a required manual pre-step (working around a real terraform-provider-helm OCI-pull bug, not optional setup) - run: helm pull oci://docker.io/envoyproxy/gateway-helm --version ${local.envoy_gateway_chart_version} -d ${path.module}/.charts   See README.md's 'Known limitation: Envoy Gateway chart pull' section for why this is required."
    }
  }
}

# ---------------------------------------------------------------------------------------------------------------------
# PHASE 2 (gated behind var.install_gateway_resources): Gateway API objects + Anyscale Operator.
# ---------------------------------------------------------------------------------------------------------------------

# The Operator's helm value wants the RAW underscored cloud_resource_id (e.g. cldrsrc_xxx). The TLS
# Secret name and certificateRefs the Operator creates use the SAME id with underscores swapped for
# dashes (cldrsrc-xxx). Getting this backwards is a SILENT failure (HTTPS listeners stick at
# ResolvedRefs=False) - both forms are load-bearing in different places, do not simplify away.
locals {
  cloud_resource_id_dashed = replace(anyscale_cloud.primary.cloud_resource_id, "_", "-")
  gateway_tls_secret_name  = "anyscale-${local.cloud_resource_id_dashed}-certificate"
}

resource "kubernetes_namespace" "anyscale_operator" {
  count = var.install_gateway_resources ? 1 : 0

  metadata {
    name = "anyscale-operator"
  }

  depends_on = [helm_release.envoy_gateway]
}

resource "kubernetes_manifest" "envoy_proxy" {
  count = var.install_gateway_resources ? 1 : 0

  manifest = {
    apiVersion = "gateway.envoyproxy.io/v1alpha1"
    kind       = "EnvoyProxy"
    metadata = {
      name      = "anyscale-envoy-proxy-config"
      namespace = "anyscale-operator"
    }
    spec = {
      provider = {
        type = "Kubernetes"
        kubernetes = {
          envoyService = {
            type = "LoadBalancer"
            # Confirmed live: do not change "nlb" back to "external" - "external" tells Kubernetes
            # to skip its own in-tree Service-LB provisioner (present on EKS as a hidden,
            # control-plane-managed component, not a visible cluster pod) and defer entirely to the
            # AWS Load Balancer Controller, which this "basic" example deliberately does not install
            # (a turnkey-everything variant with AWS LBC would be a separate aws-eks-full example).
            # With "external", the Service sits at EXTERNAL-IP <pending> forever - no error, no
            # useful event, just permanent pending. "nlb" is the legacy in-tree opt-in that predates
            # AWS LBC and still works on EKS 1.36 with zero extra controllers.
            annotations = {
              "service.beta.kubernetes.io/aws-load-balancer-type"            = "nlb"
              "service.beta.kubernetes.io/aws-load-balancer-nlb-target-type" = "ip"
              "service.beta.kubernetes.io/aws-load-balancer-scheme"          = "internet-facing"
            }
          }
        }
      }
    }
  }

  depends_on = [helm_release.envoy_gateway, kubernetes_namespace.anyscale_operator]
}

resource "kubernetes_manifest" "gateway_class" {
  count = var.install_gateway_resources ? 1 : 0

  manifest = {
    apiVersion = "gateway.networking.k8s.io/v1"
    kind       = "GatewayClass"
    metadata = {
      name = "anyscale-envoy-gateway-class"
    }
    spec = {
      controllerName = "gateway.envoyproxy.io/gatewayclass-controller"
      parametersRef = {
        group     = "gateway.envoyproxy.io"
        kind      = "EnvoyProxy"
        name      = kubernetes_manifest.envoy_proxy[0].manifest.metadata.name
        namespace = "anyscale-operator"
      }
    }
  }

  depends_on = [kubernetes_manifest.envoy_proxy]
}

resource "kubernetes_manifest" "gateway" {
  count = var.install_gateway_resources ? 1 : 0

  manifest = {
    apiVersion = "gateway.networking.k8s.io/v1"
    kind       = "Gateway"
    metadata = {
      name      = "anyscale-gateway"
      namespace = "anyscale-operator"
    }
    spec = {
      gatewayClassName = kubernetes_manifest.gateway_class[0].manifest.metadata.name
      listeners = [
        # Bootstrap HTTP:80 listener. Carries no real user traffic - exists ONLY so the Gateway can
        # reach Programmed (and thus have a readable address) BEFORE the Operator-created TLS
        # Secrets exist yet. This breaks what would otherwise be a circular dependency: the Gateway
        # needs to be Programmed to yield the hostname the Operator needs; the Operator needs to
        # run, using that hostname, before it creates the Secret the HTTPS listener below needs to
        # resolve. Preserve this listener even though nothing routes through it in practice.
        {
          name          = "bootstrap-http"
          protocol      = "HTTP"
          port          = 80
          allowedRoutes = { namespaces = { from = "All" } }
        },
        {
          name     = "https-primary"
          protocol = "HTTPS"
          port     = 443
          tls = {
            mode = "Terminate"
            certificateRefs = [
              { name = local.gateway_tls_secret_name, namespace = "anyscale-operator" }
            ]
          }
          allowedRoutes = { namespaces = { from = "All" } }
        }
      ]
    }
  }

  # Confirmed live, two real syntax requirements neither guessable from the design alone: (1) list
  # elements need BRACKET notation, "status.addresses[0].value", not dot-numeral
  # ("status.addresses.0.value" errors with "Dot must be followed by attribute name"); (2) the match
  # value is a real regex, not a glob - a bare "*" is not a valid standalone regex, match "non-empty"
  # with ".+" instead. Waiting on a non-empty address rather than a positional status.conditions[0]
  # is deliberate too - Gateway API's conditions list is unordered.
  wait {
    fields = {
      "status.addresses[0].value" = ".+"
    }
  }

  depends_on = [kubernetes_manifest.gateway_class]
}

# kubernetes_manifest.gateway[0].object is a `dynamic` computed attribute - Terraform's plan-time
# static attribute resolution has no way to know `object` will have a "status" key at all (status is
# entirely server-populated and outside the manifest supplied), so referencing `.object.status...`
# from ANOTHER resource's config fails at plan time with "Unsupported attribute", even in phase 2
# where the CRD already exists. This separate, explicit read via data "kubernetes_resource" - gated
# on the SAME wait via depends_on - sidesteps that: its own `object` result is read fresh at apply
# time, after the wait{} above has already confirmed Programmed, so the address is genuinely
# populated by the time this data source's Read runs.
data "kubernetes_resource" "gateway_status" {
  count       = var.install_gateway_resources ? 1 : 0
  api_version = "gateway.networking.k8s.io/v1"
  kind        = "Gateway"

  metadata {
    name      = "anyscale-gateway"
    namespace = "anyscale-operator"
  }

  # Load-bearing: without this, Terraform has no reason to defer this data source's read past the
  # plan phase (its api_version/kind/name/namespace are all static literals, not derived from the
  # Gateway resource), so it could read a not-yet-Programmed Gateway. depends_on forces the read to
  # wait until kubernetes_manifest.gateway's wait{} block has completed.
  depends_on = [kubernetes_manifest.gateway]
}

resource "helm_release" "anyscale_operator" {
  count = var.install_gateway_resources ? 1 : 0

  name             = "anyscale-operator"
  repository       = "https://anyscale.github.io/helm-charts"
  chart            = "anyscale-operator"
  namespace        = "anyscale-operator"
  create_namespace = false # created above by kubernetes_namespace.anyscale_operator

  values = [
    yamlencode({
      global = {
        # NAMING TRAP: the helm key is literally "cloudDeploymentId" but the value it wants is the
        # cloud RESOURCE id - RAW underscored form, e.g. cldrsrc_xxx.
        cloudDeploymentId = anyscale_cloud.primary.cloud_resource_id
        cloudProvider     = "aws"
        aws = {
          region = var.aws_region
        }
      }
      networking = {
        gateway = {
          enabled   = true
          name      = kubernetes_manifest.gateway[0].manifest.metadata.name
          namespace = "anyscale-operator"
          # DASHED form here - see local.cloud_resource_id_dashed above. Reads via the
          # data.kubernetes_resource fallback, not kubernetes_manifest.gateway[0].object.status -
          # see the comment above data.kubernetes_resource.gateway_status.
          hostname = data.kubernetes_resource.gateway_status[0].object.status.addresses[0].value
        }
      }
      workloads = {
        serviceAccount = {
          name = "anyscale-operator"
        }
      }
    })
  ]

  depends_on = [kubernetes_manifest.gateway, anyscale_cloud.primary, aws_eks_pod_identity_association.anyscale_operator]
}
