.PHONY: build run clean extensions

build: extensions
	go build -o torii .

run: build
	./torii

clean:
	rm -f torii
	rm -f extensions/torii-echo/torii-echo
	rm -f extensions/torii-time/torii-time
	rm -f extensions/torii-web/torii-web

extensions:
	cd extensions/torii-echo && go build -o torii-echo .
	cd extensions/torii-time && go build -o torii-time .
	cd extensions/torii-web && go build -o torii-web .
