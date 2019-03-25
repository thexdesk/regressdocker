slow:
	@echo 18.09.3
	@DOCKER_BUILDKIT=1 docker build --target slow -t regression:slow .
	@docker rm -f regression-slow | true
	@docker run --privileged --name regression-slow -d regression:slow
	@docker exec -it regression-slow stress

fast:
	@echo 18.06.2
	@DOCKER_BUILDKIT=1 docker build --target fast -t regression:fast .
	@docker rm -f regression-fast | true
	@docker run --privileged --name regression-fast -d regression:fast
	@docker exec -it regression-fast stress

.PHONY: slow fast
