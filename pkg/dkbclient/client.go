package dkbclient

import (
	"bufio"
	"bytes"
	"encoding/csv"
	"encoding/json"
	"errors"
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
	httpClient  *http.Client
	xsrfToken   string
	mfaId       string
	accessToken string
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

	xsrfToken, err := c.getXsrfToken()

	if err != nil {
		return err
	}
	c.xsrfToken = xsrfToken

	err = c.postLoginCredentials(username, password)
	if err != nil {
		return err
	}

	r, err := c.newRequest(http.MethodGet, "https://banking.dkb.de/api/mfa/mfa/methods?filter%5BmethodType%5D=seal_one", nil)
	if err != nil {
		return err
	}
	resp, err := c.httpClient.Do(r)
	if err != nil {
		return err
	}

	fmt.Println(resp.Status)
	buf := new(bytes.Buffer)
	buf.ReadFrom(resp.Body)
	mfaMethodResponse := MFAMethodsResponse{}

	json.Unmarshal(buf.Bytes(), &mfaMethodResponse)
	//fmt.Println(buf.String())
	recentMethod := getMostRecentlyEnrolledMFAMethod(mfaMethodResponse.Data)
	fmt.Printf("%+v\n", recentMethod)

	ch := newMFAChallenge(recentMethod.ID, c.mfaId)
	chb, _ := json.Marshal(ch)
	r, err = c.newRequest(http.MethodPost, "https://banking.dkb.de/api/mfa/mfa/challenges", bytes.NewReader(chb))
	r.Header.Set("Content-Type", "application/vnd.api+json")
	resp, err = c.httpClient.Do(r)
	if err != nil {
		return err
	}

	b, _ := io.ReadAll(resp.Body)

	cr := MFAChallengeResponse{}
	err = json.Unmarshal(b, &cr)
	if err != nil {
		return err
	}

	err = c.pollVerificationStatus(cr.Data.ID)
	if err != nil {
		return err
	}

	err = c.postToken()
	if err != nil {
		return err
	}

	r, err = c.newRequest(http.MethodGet, "https://banking.dkb.de/api/accounts/accounts", nil)
	r.Header.Set("Content-Type", "application/vnd.api+json")
	resp, err = c.httpClient.Do(r)
	if err != nil {
		return err
	}
	b, _ = io.ReadAll(resp.Body)
	fmt.Printf("%+v", string(b))
	return nil
}

// TODO: Naming (or refactoring)
// currently this does more than just posting credentials: it also sets `c.mfa_id` and `c.accessToken`
func (c *Client) postLoginCredentials(username string, password string) error {
	data := url.Values{}

	data.Add("grant_type", "banking_user_sca")
	data.Add("sca_type", "web-login")
	data.Add("username", username)
	data.Add("password", password)

	r, err := c.newRequest(http.MethodPost, "https://banking.dkb.de/api/token", strings.NewReader(data.Encode()))
	if err != nil {
		return err
	}

	resp, err := c.httpClient.Do(r)
	if err != nil {
		return err
	}

	fmt.Println(resp.Status)

	buf := new(bytes.Buffer)

	_, err = buf.ReadFrom(resp.Body)
	if err != nil {
		return err
	}

	tokenData := TokenData{}

	err = json.Unmarshal(buf.Bytes(), &tokenData)
	if err != nil {
		return err
	}

	c.mfaId = tokenData.MfaID
	c.accessToken = tokenData.AccessToken

	return nil
}

func (c *Client) postToken() error {
	data := url.Values{}

	data.Add("grant_type", "banking_user_mfa")
	data.Add("mfa_id", c.mfaId)
	data.Add("access_token", c.accessToken)

	r, err := c.newRequest(http.MethodPost, "https://banking.dkb.de/api/token", strings.NewReader(data.Encode()))
	if err != nil {
		return err
	}

	resp, err := c.httpClient.Do(r)
	if err != nil {
		return err
	}

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("POSTing token failed: %v", err)
	}
	return nil
}

func (c *Client) newRequest(method string, url string, body io.Reader) (*http.Request, error) {
	r, err := http.NewRequest(method, url, body)
	if err != nil {
		return nil, err
	}
	r.Header.Set("x-xsrf-token", c.xsrfToken)
	return r, nil
}

func (c *Client) getXsrfToken() (string, error) {
	_, err := c.httpClient.Get("https://banking.dkb.de/login")
	if err != nil {
		return "", err
	}

	u, err := url.Parse("https://banking.dkb.de")
	if err != nil {
		return "", err
	}

	t := ""
	found := false

	for _, cookie := range c.httpClient.Jar.Cookies(u) {
		if cookie.Name == "__Host-xsrf" {
			t = cookie.Value
			found = true
			break
		}
	}
	if !found {
		return "", errors.New("XSRF cookie not found")
	}
	return t, nil
}
func (c *Client) pollVerificationStatus(cid string) error {
	pollID := time.Now().UTC().UnixMilli() * 1000
	pollURL := "https://banking.dkb.de/api/mfa/mfa/challenges/" + cid

	req, err := http.NewRequest(http.MethodGet, pollURL, nil)
	if err != nil {
		return err
	}
	for i := 0; i < 60; i++ {

		pollID++

		resp, _ := c.httpClient.Do(req)

		b, _ := io.ReadAll(resp.Body)

		c := MFAChallengeResponse{}
		err := json.Unmarshal(b, &c)
		if err != nil {
			return err
		}

		err = resp.Body.Close()
		if err != nil {
			return err
		}

		if c.Data.Attributes.VerificationStatus == "processed" {
			break
		}
		time.Sleep(3000 * time.Millisecond)
	}

	return nil
}

// ParseOverview parses the financial overview status page on the DKB website
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
	Marked    string    `csv:"Umsatz abgerechnet und nicht im Saldo enthalten"` // Ignored (for now)
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

// TODO: Move models to proper package

type MFAMethodsResponse struct {
	Data []MFAMethod `json:"data"`
}

type MFAMethod struct {
	Type       string `json:"type"`
	ID         string `json:"id"`
	Attributes struct {
		MethodType                      string    `json:"methodType"`
		DeviceName                      string    `json:"deviceName"`
		Locked                          bool      `json:"locked"`
		RemainingValidationAttempts     int       `json:"remainingValidationAttempts"`
		RemainingChallenges             int       `json:"remainingChallenges"`
		MethodTypeOrder3Ds              int       `json:"methodTypeOrder3ds"`
		SealOneID                       string    `json:"sealOneId"`
		EnrolledAt                      time.Time `json:"enrolledAt"`
		Portfolio                       string    `json:"portfolio"`
		PreferredDevice                 bool      `json:"preferredDevice"`
		LockingPeriodAfterEnrollmentEnd time.Time `json:"lockingPeriodAfterEnrollmentEnd"`
	} `json:"attributes"`
}

func getMostRecentlyEnrolledMFAMethod(methods []MFAMethod) MFAMethod {
	mostRecentlyenrolledMethod := methods[0]
	for _, m := range methods {
		if m.Attributes.EnrolledAt.After(mostRecentlyenrolledMethod.Attributes.EnrolledAt) {
			mostRecentlyenrolledMethod = m
		}
	}
	return mostRecentlyenrolledMethod
}

type MFAChallenge struct {
	Data MFAChallengeData `json:"data"`
}

type MFAChallengeData struct {
	Attributes MFAChallengeDataAttributes `json:"attributes"`
	Type       string                     `json:"type"`
}

type MFAChallengeDataAttributes struct {
	MethodID   string `json:"methodId"`
	MethodType string `json:"methodType"`
	MfaID      string `json:"mfaId"`
}

func newMFAChallenge(methodId string, mfaID string) MFAChallenge {
	return MFAChallenge{Data: MFAChallengeData{Type: "mfa-challenge",
		Attributes: MFAChallengeDataAttributes{
			MethodID:   methodId,
			MethodType: "seal_one",
			MfaID:      mfaID,
		}}}
}

type MFAChallengeResponse struct {
	Data     MFAChallengeResponseData `json:"data"`
	Included []MFAMethod              `json:"included"`
}

type MFAChallengeResponseData struct {
	Type       string `json:"type"`
	ID         string `json:"id"`
	Attributes struct {
		MfaID              string `json:"mfaId"`
		MethodID           string `json:"methodId"`
		MethodType         string `json:"methodType"`
		VerificationStatus string `json:"verificationStatus"`
	} `json:"attributes"`
	Relationships struct {
		Method struct {
			Data struct {
				Type string `json:"type"`
				ID   string `json:"id"`
			} `json:"data"`
		} `json:"method"`
	} `json:"relationships"`
	Links struct {
		Self string `json:"self"`
	} `json:"links"`
}

type TokenData struct {
	AccessToken           string `json:"access_token"`
	RefreshTokenExpiresIn string `json:"refresh_token_expires_in"`
	RefreshToken          string `json:"refresh_token"`
	TokenFactorType       string `json:"token_factor_type"`
	AnonymousUserID       string `json:"anonymous_user_id"`
	Scope                 string `json:"scope"`
	IDToken               string `json:"id_token"`
	MfaID                 string `json:"mfa_id"`
	TokenType             string `json:"token_type"`
	ExpiresIn             int    `json:"expires_in"`
}

type Accounts struct {
	Data []Account `json:"data"`
}

type Account struct {
	Type       string            `json:"type"`
	Id         string            `json:"id"`
	Attributes AccountAttributes `json:"attributes"`
}

type AccountAttributes struct {
	HolderName                        string           `json:"holderName"`
	Iban                              string           `json:"iban"`
	Permissions                       []string         `json:"permissions"`
	CurrencyCode                      string           `json:"currencyCode"`
	Balance                           CurrencyValue    `json:"balance"`
	AvailableBalance                  CurrencyValue    `json:"availableBalance"`
	NearTimeBalance                   CurrencyValue    `json:"nearTimeBalance"`
	Product                           Product          `json:"product"`
	State                             string           `json:"state"`
	UpdatedAt                         string           `json:"updatedAt"`
	OpeningDate                       string           `json:"openingDate"`
	OverdraftLimit                    string           `json:"overdraftLimit"`
	OverdraftInterestRate             string           `json:"overdraftInterestRate,omitempty"`
	InterestRate                      string           `json:"interestRate"`
	UnauthorizedOverdraftInterestRate string           `json:"unauthorizedOverdraftInterestRate"`
	LastAccountStatementDate          string           `json:"lastAccountStatementDate"`
	ReferenceAccount                  ReferenceAccount `json:"referenceAccount,omitempty"`
}

type CurrencyValue struct {
	CurrencyCode string `json:"currencyCode"`
	Value        string `json:"value"`
}

type ReferenceAccount struct {
	Iban          string `json:"iban"`
	AccountNumber string `json:"accountNumber"`
	Blz           string `json:"blz"`
}
type Product struct {
	Id          string `json:"id"`
	Type        string `json:"type"`
	DisplayName string `json:"displayName"`
}
