package main

import (
	"bytes"
	"flag"
	"fmt"
	"github.com/cloudtools/ssh-cert-authority/util"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
	"io/ioutil"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"
)

const buildVersion string = "dev"

func main() {
	var environment string

	home := os.Getenv("HOME")
	if home == "" {
		home = "/"
	}
	configPath := home + "/.ssh_ca/requester_config.json"

	flag.StringVar(&environment, "environment", "", "The environment you want (e.g. prod).")
	printVersion := flag.Bool("version", false, "Print the version and exit")
	flag.Parse()

	if *printVersion {
		fmt.Printf("sign_cert v.%s\n", buildVersion)
		os.Exit(0)
	}

	if len(flag.Args()) < 1 {
		fmt.Println("Usage: get_cert [--environment env] cert-request-id")
		os.Exit(1)
	}
	certRequestID := flag.Args()[0]

	allConfig := make(map[string]ssh_ca_util.RequesterConfig)
	err := ssh_ca_util.LoadConfig(configPath, &allConfig)
	if err != nil {
		fmt.Println("Load Config failed:", err)
		os.Exit(1)
	}

	if certRequestID == "" {
		fmt.Println("Specify --cert-request-id")
		os.Exit(1)
	}

	if len(allConfig) > 1 && environment == "" {
		fmt.Println("You must tell me which environment to use.", len(allConfig))
		os.Exit(1)
	}
	if len(allConfig) == 1 && environment == "" {
		for environment = range allConfig {
			// lame way of extracting first and only key from a map?
		}
	}
	config, ok := allConfig[environment]
	if !ok {
		fmt.Println("Requested environment not found in config file")
		os.Exit(1)
	}

	getResp, err := http.Get(config.SignerUrl + "cert/requests/" + certRequestID)
	if err != nil {
		fmt.Println("Didn't get a valid response", err)
		os.Exit(1)
	}
	getRespBuf, err := ioutil.ReadAll(getResp.Body)
	if err != nil {
		fmt.Println("Error reading response body", err)
		os.Exit(1)
	}
	getResp.Body.Close()
	if getResp.StatusCode != 200 {
		fmt.Println("Error getting that request id:", string(getRespBuf))
		os.Exit(1)
	}

	pubKey, _, _, _, err := ssh.ParseAuthorizedKey(getRespBuf)
	if err != nil {
		fmt.Println("Trouble parsing response", err)
		os.Exit(1)
	}
	cert := pubKey.(*ssh.Certificate)

	agentConn, err := net.Dial("unix", os.Getenv("SSH_AUTH_SOCK"))
	if err != nil {
		fmt.Println("Dial failed:", err)
		os.Exit(1)
	}
	sshAgent := agent.NewClient(agentConn)
	// We genuinely don't care if this fails, its not actionable
	sshAgent.Remove(cert.Key)
	pubKeyPath, err := findKeyLocally(cert.Key)

	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
	pubKeyPath = strings.Replace(pubKeyPath, ".pub", "-cert.pub", 1)
	err = ioutil.WriteFile(pubKeyPath, getRespBuf, 0644)
	if err != nil {
		fmt.Printf("Couldn't write certificate file to %s: %s\n", pubKeyPath, err)
	}

	secondsRemaining := int64(cert.ValidBefore) - int64(time.Now().Unix())
	if secondsRemaining < 1 {
		fmt.Println("This certificate has already expired.")
		os.Exit(1)
	}
	ssh_ca_util.PrintForInspection(*cert)
	privKeyPath := strings.Replace(pubKeyPath, "-cert.pub", "", 1)
	cmd := exec.Command("ssh-add", "-t", fmt.Sprintf("%d", secondsRemaining), privKeyPath)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	err = cmd.Run()
	if err != nil {
		fmt.Println("Error in ssh-add")
		os.Exit(1)
	}
}

func findKeyLocally(key ssh.PublicKey) (string, error) {
	sshDir := os.Getenv("HOME") + "/.ssh"
	dirEntries, err := ioutil.ReadDir(sshDir)
	if err != nil {
		return "", fmt.Errorf("Could not read your .ssh directory %s: %s\n", sshDir, err)
	}
	for idx := range dirEntries {
		entry := dirEntries[idx]
		if strings.HasSuffix(entry.Name(), ".pub") {
			pubKeyPath := sshDir + "/" + entry.Name()
			pubBuf, err := ioutil.ReadFile(pubKeyPath)
			if err != nil {
				return "", fmt.Errorf("Trouble reading public key %s: %s\n", pubKeyPath, err)
			}
			pubKey, _, _, _, err := ssh.ParseAuthorizedKey(pubBuf)
			if err != nil {
				return "", fmt.Errorf("Trouble parsing public key %s: %s\n", pubKeyPath, err)
			}
			if bytes.Equal(pubKey.Marshal(), key.Marshal()) {
				return pubKeyPath, nil
			}
		}
	}
	return "", fmt.Errorf("Couldn't find ssh key for cert.\n")
}