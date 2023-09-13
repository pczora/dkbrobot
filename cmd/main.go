package main

import (
	"fmt"
	"github.com/pczora/dkbrobot/pkg/dkbclient"
	"golang.org/x/term"
	"os"
	"syscall"
)

func main() {
	var username string
	var password string

	fmt.Printf("Username: ")
	_, err := fmt.Scanf("%s", &username)
	if err != nil {
		panic(err)
	}

	fmt.Printf("Password: ")
	bytepw, err := term.ReadPassword(syscall.Stdin)
	if err != nil {
		os.Exit(1)
	}
	fmt.Print("\n")

	password = string(bytepw)

	c := dkbclient.New()

	err = c.Login(username, password)
	if err != nil {
		panic(err)
	}

	accounts, err := c.GetAccounts()
	if err != nil {
		panic(err)
	}

	fmt.Printf("%+v", accounts)

}
