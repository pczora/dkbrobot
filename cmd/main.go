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
	_, err := fmt.Scanf("%s", &username)
	if err != nil {
		panic(err)
	}

	fmt.Printf("Password: ")
	bytepw, err := term.ReadPassword(int(syscall.Stdin))
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

	accounts, err := c.ParseOverview()
	if err != nil {
		panic(err)
	}

	for _, a := range accounts {
		if a.AccountType == "depot" {
			continue
		}

		if a.AccountType == "account" {
			transactions, err := c.GetAccountTransactions(a, time.Now().Add(30*-time.Hour*24), time.Now())
			if err != nil {
				panic(err)
			}
			for _, transaction := range transactions {
				fmt.Println(transaction)
			}
		} else if a.AccountType == "credit card" {
			transactions, err := c.GetCreditCardTransactions(a, time.Now().Add(30*-time.Hour*24), time.Now())
			if err != nil {
				panic(err)
			}
			for _, transaction := range transactions {
				fmt.Printf("%+v\n", transaction)
			}
		}

	}

}
