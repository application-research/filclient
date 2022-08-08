FROM golang:1.17-stretch AS builder
USER root
RUN apt-get update && \
    apt-get install -y wget jq hwloc ocl-icd-opencl-dev git libhwloc-dev pkg-config make && \
    apt-get install -y cargo
WORKDIR /app/

RUN curl https://sh.rustup.rs -sSf | bash -s -- -y
ENV PATH="/root/.cargo/bin:${PATH}"
RUN cargo --help
ARG TAG=${TAG}
RUN echo ${TAG}
RUN git clone https://github.com/application-research/filclient . && \
    git pull && \
    git fetch --all --tags && \
    git checkout ${TAG}
COPY build .
COPY extern .
USER 1001