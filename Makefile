.PHONY: build run clean extensions release install uninstall

INSTALL_DIR = $(HOME)/.local/share/torii
CONFIG_DIR  = $(HOME)/.config/torii
BIN_DIR     = $(HOME)/.local/bin

build: extensions
	go build -o torii .

run: build
	./torii

clean:
	rm -f torii
	rm -f extensions/torii-echo/torii-echo
	rm -f extensions/torii-time/torii-time
	rm -f extensions/torii-web/torii-web
	rm -f extensions/torii-search/torii-search
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

install: build
	@echo "Installing torii..."
	@mkdir -p "$(INSTALL_DIR)/extensions" "$(CONFIG_DIR)" "$(BIN_DIR)"
	@cp torii "$(INSTALL_DIR)/torii"
	@codesign -s - "$(INSTALL_DIR)/torii" 2>/dev/null || true
	@for dir in extensions/*/; do \
		name=$$(basename "$$dir"); \
		mkdir -p "$(INSTALL_DIR)/extensions/$$name"; \
		cp "$$dir/manifest.json" "$(INSTALL_DIR)/extensions/$$name/" 2>/dev/null || true; \
		[ -x "$$dir/$$name" ] && cp "$$dir/$$name" "$(INSTALL_DIR)/extensions/$$name/" || true; \
	done
	@if [ ! -f "$(CONFIG_DIR)/config.yaml" ]; then \
		cp config.yaml.example "$(CONFIG_DIR)/config.yaml"; \
		echo "Created $(CONFIG_DIR)/config.yaml — edit it before starting torii"; \
	else \
		echo "Config already exists at $(CONFIG_DIR)/config.yaml (not overwritten)"; \
	fi
	@ln -sf "$(INSTALL_DIR)/torii" "$(BIN_DIR)/torii"
	@OS=$$(uname -s); \
	if [ "$$OS" = "Linux" ]; then \
		mkdir -p "$(HOME)/.config/systemd/user"; \
		printf '[Unit]\nDescription=Torii AI Assistant\nAfter=network.target\n\n[Service]\nType=simple\nExecStart=%%h/.local/share/torii/torii\nWorkingDirectory=%%h/.local/share/torii\nRestart=on-failure\nRestartSec=5\n\n[Install]\nWantedBy=default.target\n' \
			> "$(HOME)/.config/systemd/user/torii.service"; \
		systemctl --user daemon-reload; \
		systemctl --user enable --now torii; \
		echo "Systemd user service enabled and started"; \
	elif [ "$$OS" = "Darwin" ]; then \
		mkdir -p "$(HOME)/Library/LaunchAgents"; \
		printf '<?xml version="1.0" encoding="UTF-8"?>\n<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">\n<plist version="1.0">\n<dict>\n  <key>Label</key>\n  <string>dev.harato.torii</string>\n  <key>ProgramArguments</key>\n  <array>\n    <string>%s/.local/share/torii/torii</string>\n  </array>\n  <key>WorkingDirectory</key>\n  <string>%s/.local/share/torii</string>\n  <key>RunAtLoad</key>\n  <true/>\n  <key>KeepAlive</key>\n  <true/>\n  <key>StandardOutPath</key>\n  <string>%s/.local/share/torii/torii.log</string>\n  <key>StandardErrorPath</key>\n  <string>%s/.local/share/torii/torii.log</string>\n</dict>\n</plist>\n' "$(HOME)" "$(HOME)" "$(HOME)" "$(HOME)" \
			> "$(HOME)/Library/LaunchAgents/dev.harato.torii.plist"; \
		launchctl load "$(HOME)/Library/LaunchAgents/dev.harato.torii.plist" 2>/dev/null || true; \
		echo "LaunchAgent loaded and started"; \
	fi
	@echo "Done! Make sure $(BIN_DIR) is in your PATH."

uninstall:
	@echo "Uninstalling torii..."
	@OS=$$(uname -s); \
	if [ "$$OS" = "Linux" ]; then \
		systemctl --user disable --now torii 2>/dev/null || true; \
		rm -f "$(HOME)/.config/systemd/user/torii.service"; \
		systemctl --user daemon-reload 2>/dev/null || true; \
		echo "Systemd service removed"; \
	elif [ "$$OS" = "Darwin" ]; then \
		launchctl unload "$(HOME)/Library/LaunchAgents/dev.harato.torii.plist" 2>/dev/null || true; \
		rm -f "$(HOME)/Library/LaunchAgents/dev.harato.torii.plist"; \
		echo "LaunchAgent removed"; \
	fi
	@rm -f "$(BIN_DIR)/torii"
	@rm -rf "$(INSTALL_DIR)"
	@echo "Removed $(INSTALL_DIR) and $(BIN_DIR)/torii"
	@echo "Config preserved at $(CONFIG_DIR)/ — remove manually if desired"

extensions:
	cd extensions/torii-echo && go build -o torii-echo .
	cd extensions/torii-time && go build -o torii-time .
	cd extensions/torii-web && go build -o torii-web .
	cd extensions/torii-search && go build -o torii-search .
