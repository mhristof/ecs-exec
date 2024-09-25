
bin/ecs-exec: main.go go.mod go.sum
	go build 
	mv ecs-exec bin/

install: bin/ecs-exec
	mv bin/ecs-exec ~/.local/bin/
