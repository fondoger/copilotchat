package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"
)

func main() {
	socket := os.Getenv("MYGO_AUTOMATION")
	if socket == "" {
		socket = filepath.Join(os.TempDir(), "copilotchat-automation.sock")
	}
	address := flag.String("socket", socket, "MyGo automation Unix socket")
	timeout := flag.Duration("timeout", 10*time.Second, "Connection and response timeout")
	printSocket := flag.Bool("print-socket", false, "Print the resolved socket path without connecting")
	flag.Usage = func() {
		fmt.Fprintln(flag.CommandLine.Output(), "Usage: automation [flags] method ['{\"window\":1}']")
		flag.PrintDefaults()
	}
	flag.Parse()
	if *printSocket {
		fmt.Println(*address)
		return
	}
	if flag.NArg() < 1 || flag.NArg() > 2 || *timeout <= 0 {
		flag.Usage()
		os.Exit(2)
	}
	var params json.RawMessage
	if flag.NArg() == 2 {
		params = json.RawMessage(flag.Arg(1))
		if !json.Valid(params) {
			fmt.Fprintln(os.Stderr, "params must be valid JSON")
			os.Exit(2)
		}
	}
	result, err := call(*address, flag.Arg(0), params, *timeout)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println(string(result))
}

func call(socket, method string, params json.RawMessage, timeout time.Duration) (json.RawMessage, error) {
	deadline := time.Now().Add(timeout)
	conn, err := net.DialTimeout("unix", socket, timeout)
	if err != nil {
		return nil, fmt.Errorf("connect to automation socket: %w", err)
	}
	defer conn.Close()
	if err := conn.SetDeadline(deadline); err != nil {
		return nil, err
	}
	request := struct {
		ID     int             `json:"id"`
		Method string          `json:"method"`
		Params json.RawMessage `json:"params,omitempty"`
	}{1, method, params}
	if err := json.NewEncoder(conn).Encode(request); err != nil {
		return nil, fmt.Errorf("send automation request: %w", err)
	}
	var response struct {
		ID     int             `json:"id"`
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.NewDecoder(conn).Decode(&response); err != nil {
		return nil, fmt.Errorf("read automation response: %w", err)
	}
	if response.ID != request.ID {
		return nil, errors.New("automation response ID does not match request")
	}
	if response.Error != nil {
		return nil, fmt.Errorf("%s: %s", response.Error.Code, response.Error.Message)
	}
	if len(response.Result) == 0 {
		return nil, errors.New("automation response has no result")
	}
	return response.Result, nil
}
