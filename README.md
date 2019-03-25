regressdocker
---

regressdocker orchestrates bootstrapping (starting state) and a stress test for [docker](https://github.com/docker/docker) to measure how long client calls make between different versions.

This project was created to discover if dockerd introduces performance regressions.

## Usage

Run `make slow` to measure with `18.09.3` or `make fast` for `18.06.2`. The end goal is to compare an arbitrary list of versions against the same bootstrapping/stress tests.

Assumes host system has `docker >= 18.09.3` with [buildkit](https://github.com/moby/buildkit) enabled.
