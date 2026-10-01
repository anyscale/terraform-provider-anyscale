# Import using the cluster environment ID
terraform import anyscale_container_image_build.example cenv_abc123

# Import recovers `project_id`. Set it in your config if the image was built in a project, and
# omit it otherwise; a mismatch plans a replacement.
