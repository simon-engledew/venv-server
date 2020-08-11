FROM ubuntu:xenial

ADD root/ /

RUN apt-get update \
	&& apt-get install --quiet --no-install-recommends --yes locales python3.8 python3.8-dev python3.8-venv \
	&& locale-gen en_GB.UTF-8

ADD pypi /pypi

ENV LANG=en_GB.UTF-8 LANGUAGE=en_GB:en

ARG VENV=/opt/venv

RUN python3.8 -m venv $VENV \
    && $VENV/bin/python3.8 -m pip install --no-deps --require-hashes -r /pypi/requirements.txt

ADD /requirements.txt /

RUN $VENV/bin/python3.8 -m pip install --no-deps --require-hashes -r /requirements.txt
