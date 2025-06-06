# README

## About

This is the official Wails React-TS template.

You can configure the project by editing `wails.json`. More information about the project settings can be found
here: https://wails.io/docs/reference/project-config

## Live Development

To run in live development mode, run `wails dev` in the project directory. This will run a Vite development
server that will provide very fast hot reload of your frontend changes. If you want to develop in a browser
and have access to your Go methods, there is also a dev server that runs on http://localhost:34115. Connect
to this in your browser, and you can call your Go code from devtools.

### Config File

Git currently ignores the runtime config. So copy the `example.config.yaml` to `config.yaml` and edit it to your needs.

The config file is also editable through the Server GUI.

### Building Protobuf files

To build a `*.proto` file, you need to install the `protoc` compiler and the Go plugin for it. You can do this by running:

```bash
protoc --go_out=./srspb --go_opt=paths=source_relative srspb.proto
```

## Building

To build a redistributable, production mode package, use `wails build`.

## Running

The built application can be found in the `build/bin` directory. You can run it by executing the binary file.

### Flags
You can pass flags to the application by using the `--` separator. Following Flags are available:
- `--config /path/to/config.yaml` - Path to the config file. Default is `./config.yaml`
- `--autostart` - If the servers should be started automatically. Default is `false`


## Improvements

* Use a Go Module for the Protobuf files / Generated Go code
  * Use this when the C# Client is finished and the Go Client is ready