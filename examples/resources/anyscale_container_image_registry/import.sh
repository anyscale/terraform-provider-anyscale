# Import using the cluster environment ID
terraform import anyscale_container_image_registry.example cenv_abc123

# Import does not recover `registry_login_secret`. If your config sets it, the first plan after
# import records it in state with a warning and does not replace the image.
