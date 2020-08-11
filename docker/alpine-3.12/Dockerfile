FROM alpine:3.12.0

RUN apk add --no-cache python3 python3-dev

ADD pypi /pypi

ARG VENV=/opt/venv

RUN python3.8 -m venv $VENV \
    && $VENV/bin/python3.8 -m pip install --no-deps --require-hashes -r /pypi/requirements.txt

ADD /requirements.txt /

RUN $VENV/bin/python3.8 -m pip install --no-deps --require-hashes -r /requirements.txt
