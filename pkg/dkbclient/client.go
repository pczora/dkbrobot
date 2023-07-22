package dkbclient

import (
	"bufio"
	"bytes"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
	"github.com/gocarina/gocsv"
	"golang.org/x/text/encoding/charmap"
)

func init() {
	gocsv.SetCSVReader(func(in io.Reader) gocsv.CSVReader {
		// DKB uses ISO8859-15 (for whatever reason)
		reader := csv.NewReader(charmap.ISO8859_15.NewDecoder().Reader(in))
		reader.Comma = ';'
		return reader
	})
}

// Client is an HTTP client for the DKB web interface
type Client struct {
	httpClient *http.Client
}

// AccountMetadata represents some metadata about a (financial) account as part
// of a user's DKB account
type AccountMetadata struct {
	AccountName     string
	Account         string
	Date            string
	Amount          string
	AccountType     AccountType
	TransactionLink string
}

// AccountType determines the type of an account
type AccountType int64

const (
	// CheckingAccount represents a checking account
	CheckingAccount AccountType = iota
	// Depot represents a stock share depot
	Depot
	// CreditCard represents a credit card
	CreditCard
)

// New creates a new Client
func New() Client {
	jar, err := cookiejar.New(nil)
	if err != nil {
		panic(err)
	}
	httpClient := &http.Client{Jar: jar, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		fmt.Println("redirect")
		fmt.Println(req.URL)
		return nil
	}}

	return Client{httpClient: httpClient}
}

// Login logs in to the DKB website using the provided credentials
func (c *Client) Login(username, password string) error {

	resp, err := c.httpClient.Get("https://www.ib.dkb.de/banking")
	if err != nil {
		panic(err)
	}
	d, err := goquery.NewDocumentFromReader(resp.Body)
	if err != nil {
		return fmt.Errorf("could not create document from reponse: %v", err)
	}

	sid, _ := d.Find("form#login input[name='$sID$']").Attr("value")
	token, _ := d.Find("form#login input[name='token']").Attr("value")

	data := url.Values{}

	data.Add("$sID$", sid)
	data.Add("token", token)
	data.Add("j_username", username)
	data.Add("j_password", password)

	resp, err = c.httpClient.PostForm("https://www.ib.dkb.de/banking", data)
	if err != nil {
		return err
	}

	doc, err := goquery.NewDocumentFromReader(resp.Body)
	if err != nil {
		return err
	}

	xsrfToken, _ := doc.Find("input[name='XSRFPreventionToken']").Attr("value")
	action, _ := doc.Find("form#confirmForm").Attr("action")
	err = c.pollVerification(action, xsrfToken)
	if err != nil {
		return err
	}
	return nil
}

func (c *Client) pollVerification(confirmFormAction string, xsrfToken string) error {

	pollID := time.Now().UTC().UnixMilli() * 1000
	pollURL := "https://www.ib.dkb.de" + confirmFormAction

	for i := 0; i < 60; i++ {

		pollID++

		fullPollURL := pollURL + "?$event=pollingVerification&$ignore.request=true&_=" + strconv.Itoa(int(pollID))

		resp, _ := c.httpClient.Get(fullPollURL)

		b, _ := io.ReadAll(resp.Body)

		bodyJSON := make(map[string]string)
		err := json.Unmarshal(b, &bodyJSON)
		if err != nil {
			return err
		}

		if bodyJSON["state"] == "PROCESSED" {
			fmt.Println("success")
			break
		}

		err = resp.Body.Close()
		if err != nil {
			return err
		}

		time.Sleep(3000 * time.Millisecond)
	}

	postData := url.Values{}
	postData.Add("$event", "next")
	postData.Add("XSRFPreventionToken", xsrfToken)

	resp, err := c.httpClient.PostForm(pollURL, postData)
	if err != nil {
		return err
	}

	fmt.Println(resp.Status)

	return nil
}

// ParseOverview parses the financial over view status page on the DKB website
// and returns a slice containing data about each parsed account
func (c *Client) ParseOverview() ([]AccountMetadata, error) {
	resp, err := c.httpClient.Get("https://www.dkb.de/DkbTransactionBanking/content/banking/financialstatus/FinancialComposite/FinancialStatus.xhtml?$event=init")
	if err != nil {
		return nil, err
	}

	d, err := goquery.NewDocumentFromReader(resp.Body)

	if err != nil {
		return nil, err
	}

	rows := d.Find("tr.mainRow")

	var result []AccountMetadata

	rows.Each(func(i int, row *goquery.Selection) {
		var (
			accountName     string
			account         string
			date            string
			amount          string
			accountType     AccountType
			transactionLink string
		)
		cols := row.Find("td")
		cols.Each(func(i int, col *goquery.Selection) {
			switch i {
			case 0:
				accountName = strings.TrimSpace(col.Find("div.forceWrap").Text())
				break
			case 1:
				account = strings.TrimSpace(col.Find("div.iban").Text())
				break
			case 2:
				date = strings.TrimSpace(col.Text())
				break
			case 3:
				amount = strings.TrimSpace(col.Text())
				break
			case 4:
				link := col.Find("a.evt-paymentTransaction")
				if len(link.Nodes) > 0 {
					if strings.HasPrefix(account, "DE") {
						accountType = CheckingAccount
					} else {
						accountType = CreditCard
					}

					transactionLink, _ = link.Attr("href")
				} else {
					accountType = Depot
					transactionLink, _ = col.Find("a.evt-depot").Attr("href")
				}
				break
			default:
			}
		})
		result = append(result, AccountMetadata{
			AccountName:     accountName,
			Account:         account,
			Date:            date,
			Amount:          amount,
			AccountType:     accountType,
			TransactionLink: transactionLink,
		})
	})
	return result, nil
}

// DkbTime represents a time format which is used by the DKB
type DkbTime time.Time

// MarshalCSV marshals DkbTime object to CSV
func (date *DkbTime) MarshalCSV() (string, error) {
	return time.Time(*date).Format("02.01.2006"), nil
}

// UnmarshalCSV unmarshals DkbTime from CSV
func (date *DkbTime) UnmarshalCSV(csv string) (err error) {
	t, err := time.Parse("02.01.2006", csv)
	*date = DkbTime(t)
	return err
}

// DkbAmount represents an amount of money as formatted by the DKB website
type DkbAmount float64

// MarshalCSV marshals DkbAmount to CSV
func (amount *DkbAmount) MarshalCSV() (string, error) {
	return strconv.FormatFloat(float64(*amount), 'f', 2, 64), nil
}

// UnmarshalCSV unmarshals CSV to DkbAmount
func (amount *DkbAmount) UnmarshalCSV(csv string) (err error) {
	normalizedAmount := amount.normalizeAmount(csv)
	floatAmount, err := strconv.ParseFloat(normalizedAmount, 64)
	if err != nil {
		return err
	}
	*amount = DkbAmount(floatAmount)
	return nil
}

func (amount *DkbAmount) normalizeAmount(a string) string {
	result := strings.ReplaceAll(a, ".", "")
	result = strings.ReplaceAll(result, ",", ".")
	return result
}

// AccountTransaction represents a transaction in a DKB checking account
type AccountTransaction struct {
	Date              DkbTime   `csv:"Buchungstag"`
	ValueDate         DkbTime   `csv:"Wertstellung"`
	PostingText       string    `csv:"Buchungstext"`
	Payee             string    `csv:"Auftraggeber / Begünstigter"`
	Purpose           string    `csv:"Verwendungszweck"`
	BankAccountNumber string    `csv:"Kontonummer"`
	BankCode          string    `csv:"Bankleitzahl"`
	Amount            DkbAmount `csv:"Betrag (EUR)"`
	CreditorID        string    `csv:"Gläubiger-ID"`
	MandateReference  string    `csv:"Mandatsreferenz"`
	CustomerReference string    `csv:"Kundenreferenz"`
}

// CreditCardTransaction represents a transaction in a DKB credit card account
type CreditCardTransaction struct {
	Marked    string    `csv:"Umsatz abgerechnet aber nicht im Saldo enthalten"` // Ignored (for now)
	ValueDate DkbTime   `csv:"Wertstellung"`
	Date      DkbTime   `csv:"Belegdatum"`
	Purpose   string    `csv:"Beschreibung"`
	Amount    DkbAmount `csv:"Betrag (EUR)"`
	//OriginalAmount DkbAmount   `csv:"Ursprünglicher Betrag"` // Ignored (for now)
}

// GetAccountTransactions returns all transactions in the checking account identified
// by the provided AccountMetadata between the `from` and `to` times
func (c *Client) GetAccountTransactions(a AccountMetadata, from time.Time, to time.Time) ([]AccountTransaction, error) {
	result, err := c.getTransactions(a, from, to)
	if err != nil {
		return nil, err
	}

	return c.parseAccountTransactions(result)
}

// GetCreditCardTransactions returns all transactions in the credit card account
// identified by the provided AccountMetadata between the `from` and `to` times
func (c *Client) GetCreditCardTransactions(a AccountMetadata, from time.Time, to time.Time) ([]CreditCardTransaction, error) {
	result, err := c.getTransactions(a, from, to)
	if err != nil {
		return nil, err
	}

	return c.parseCreditCardTransactions(result)
}

func (c *Client) getTransactions(a AccountMetadata, from time.Time, to time.Time) ([]byte, error) {
	resp, err := c.httpClient.Get("https://www.dkb.de" + a.TransactionLink)
	if err != nil {
		return nil, err
	}

	d, err := goquery.NewDocumentFromReader(resp.Body)

	if err != nil {
		return nil, err
	}
	accountNumber, _ := d.Find("select[name='slAllAccounts'] option[selected='selected']").Attr("value")

	postURL := "https://www.dkb.de/banking/finanzstatus/kontoumsaetze"
	postData := url.Values{}
	postData.Add("slTransactionStatus", "0")
	postData.Add("slSearchPeriod", "1")
	postData.Add("searchPeriodRadio", "1")
	postData.Add("transactionDate", from.Format("02.01.2006"))
	postData.Add("toTransactionDate", to.Format("02.01.2006"))
	postData.Add("$event", "search")
	postData.Add("slAllAccounts", accountNumber)

	resp, err = c.httpClient.PostForm(postURL, postData)

	if err != nil {
		return nil, err
	}

	// TODO: Clean this up
	url := "https://www.dkb.de/banking/finanzstatus/kontoumsaetze?$event=csvExport"
	if a.AccountType == CreditCard {
		url = "https://www.dkb.de/banking/finanzstatus/kreditkartenumsaetze?$event=csvExport"
	}
	resp, err = c.httpClient.Get(url)

	if err != nil {
		return nil, err
	}

	result, err := io.ReadAll(resp.Body)

	if err != nil {
		return nil, err
	}
	return result, nil
}

func (c *Client) parseAccountTransactions(transactions []byte) ([]AccountTransaction, error) {
	var result []AccountTransaction

	r := bufio.NewReader(bytes.NewReader(transactions))
	skipLines(r, 6) // First six lines consist of metadata

	err := gocsv.Unmarshal(r, &result)
	if err != nil {
		return nil, err
	}

	return result, nil
}

func (c *Client) parseCreditCardTransactions(transactions []byte) ([]CreditCardTransaction, error) {
	var result []CreditCardTransaction

	r := bufio.NewReader(bytes.NewReader(transactions))
	skipLines(r, 6) // First six lines consist of metadata

	err := gocsv.Unmarshal(r, &result)
	if err != nil {
		return nil, err
	}

	return result, nil
}

func skipLines(r *bufio.Reader, n int) {
	for i := 0; i < n; i++ {
		r.ReadLine()
	}
}
