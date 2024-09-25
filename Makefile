
bin/ecs-exec: main.go go.mod go.sum
	go build 
	mkdir -p bin
	mv ecs-exec bin/

install: bin/ecs-exec
	mv bin/ecs-exec ~/.local/bin/
