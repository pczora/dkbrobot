package main

import (
	"fmt"
	"github.com/pczora/dkbgobot/pkg/dkbclient"
	"golang.org/x/term"
	"os"
	"syscall"
	"time"
)

func main() {
	var username string
	var password string

	fmt.Printf("Username: ")
	fmt.Scanf("%s", &username)
	fmt.Printf("Password: ")
	bytepw, err := term.ReadPassword(int(syscall.Stdin))
	if err != nil {
		os.Exit(1)
	}
	password = string(bytepw)

	c := dkbclient.New()

	err = c.Login(username, password)
	if err != nil {
		panic(err)
	}

	accounts, err := c.ParseOverview()
	if err != nil {
		panic(err)
	}

	for _, a := range accounts {
		if a.AccountType == "depot" {
			continue
		}

		transactions, err := c.GetAccountTransactions(a, time.Now().Add(30*-time.Hour*24), time.Now())
		if err != nil {
			panic(err)
		}

		err = os.WriteFile(a.Account+".csv", []byte(transactions), os.ModePerm)
		if err != nil {
			panic(err)
		}
	}

}
