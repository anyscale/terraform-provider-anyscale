# The "handle" to give Anyscale job/service submission tooling (the CLI, SDK, or web
# console) is name_version, not image_uri or id -- it encodes both the image's name and
# the specific revision that was built. See the "Container Images" guide for why.
output "container_image_name_version" {
  description = "The name:revision handle for this container image. Pass this to Anyscale job/service submission tooling to pin a workload to this exact build."
  value       = anyscale_container_image_build.example.name_version
}

# image_uri is the raw, pullable image reference (e.g. what you'd "docker pull"). It's
# useful for inspecting the image directly, but name_version above is the Anyscale-native
# identifier -- prefer it when submitting jobs or services.
output "container_image_uri" {
  description = "The raw, pullable image URI. Provided for reference/inspection; prefer container_image_name_version when submitting jobs or services."
  value       = anyscale_container_image_build.example.image_uri
}

output "container_image_build_status" {
  description = "The current status of the build (pending, in_progress, succeeded, failed, pending_cancellation, canceled)."
  value       = anyscale_container_image_build.example.build_status
}

# Pass this alongside container_image_name_version when submitting a job or service --
# together they specify what to run and where to run it.
output "compute_config_name_version" {
  description = "The name:revision handle for this compute config. Pass this to Anyscale job/service submission tooling alongside the container image handle above."
  value       = anyscale_compute_config.example.name_version
}

output "compute_config_id" {
  description = "The ID of the created compute config."
  value       = anyscale_compute_config.example.id
}
