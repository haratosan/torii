.PHONY: build run clean extensions release

build: extensions
	go build -o torii .

run: build
	./torii

clean:
	rm -f torii
	rm -f extensions/torii-echo/torii-echo
	rm -f extensions/torii-time/torii-time
	rm -f extensions/torii-web/torii-web
	rm -rf release/

release: build
	mkdir -p release/extensions
	cp torii release/
	cp config.yaml.example release/config.yaml.example
	@for dir in extensions/*/; do \
		name=$$(basename "$$dir"); \
		mkdir -p "release/extensions/$$name"; \
		cp "$$dir/manifest.json" "release/extensions/$$name/" 2>/dev/null || true; \
		[ -x "$$dir/$$name" ] && cp "$$dir/$$name" "release/extensions/$$name/" || true; \
	done

extensions:
	cd extensions/torii-echo && go build -o torii-echo .
	cd extensions/torii-time && go build -o torii-time .
	cd extensions/torii-web && go build -o torii-web .
