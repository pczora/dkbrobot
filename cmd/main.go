package main

import (
	"fmt"
	"github.com/pczora/dkbrobot/pkg/dkbclient"
	"golang.org/x/term"
	"os"
	"strings"
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

	err = c.Login(username, password, dkbclient.GetMostRecentlyEnrolledMFAMethod)
	if err != nil {
		panic(err)
	}

	//accounts, err := c.GetAccounts()
	//if err != nil {
	//	panic(err)
	//}
	//
	//fmt.Printf("%+v", accounts)

	documents, err := c.GetDocuments()

	if err != nil {
		panic(err)
	}

	for _, d := range documents.Data {
		fmt.Printf("%+v\n", d)
		data, err := c.GetDocumentData(d.ID)
		if err != nil {
			panic(err)
		}

		filename := d.Attributes.FileName
		if !strings.HasSuffix(filename, ".pdf") {
			filename = filename + ".pdf"
		}

		f, err := os.Create(filename)
		if err != nil {
			panic(err)
		}
		f.Write(data)
		f.Close()

	}

}
