# venv-server

Build and serve Python virtual environments built for different distributions.

Post a requirements file to the service and get back a tar of a virtualenv:

```bash
# POST /<IMAGE>/<...DEST>

curl --silent --data-binary "@requirements.txt" http://localhost:8080/xenial/opt/venv | tar xp -C /
```

Images are loaded from a `docker` directory. There are two examples available for alpine and ubuntu.
