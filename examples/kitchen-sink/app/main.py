# Ray Serve hello world, deployed by service.tf as the service's working_dir.
from ray import serve


@serve.deployment
class Hello:
    def __call__(self, request) -> str:
        return "Hello from the kitchen-sink service"


app = Hello.bind()
