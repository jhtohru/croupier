package money

import (
	"encoding/json"
	"errors"
	"math"
	"regexp"
	"strconv"
	"strings"

	"golang.org/x/text/currency"
)

var (
	ErrInvalidAmount    = errors.New("invalid amount")
	ErrInvalidCurrency  = errors.New("invalid currency")
	ErrCurrencyMismatch = errors.New("currency mismatch")
	ErrOverflow         = errors.New("integer overflow")
)

type Money struct {
	currency string
	amount   int64
}

func New(currency, amountStr string) (Money, error) {
	if err := validateCurrency(currency); err != nil {
		return Money{}, err
	}
	amount, err := parseAmountString(amountStr)
	if err != nil {
		return Money{}, err
	}
	return Money{currency: currency, amount: amount}, nil
}

func FromMinorUnits(currency string, amount int64) (Money, error) {
	if err := validateCurrency(currency); err != nil {
		return Money{}, err
	}
	return Money{currency: currency, amount: amount}, nil
}

func Zero(curr string) (Money, error) {
	return FromMinorUnits(curr, 0)
}

func (m Money) Negate() (Money, error) {
	if m.amount == math.MinInt64 {
		return Money{}, ErrOverflow
	}
	return Money{currency: m.currency, amount: -m.amount}, nil
}

func (m Money) Add(n Money) (Money, error) {
	if m.currency != n.currency {
		return Money{}, ErrCurrencyMismatch
	}
	a := m.amount
	b := n.amount
	c := a + b
	if (b > 0 && c < a) || (b < 0 && c > a) {
		return Money{}, ErrOverflow
	}
	return Money{currency: m.currency, amount: m.amount + n.amount}, nil
}

func (m Money) Subtract(n Money) (Money, error) {
	n, err := n.Negate()
	if err != nil {
		return Money{}, err
	}
	return m.Add(n)
}

func (m Money) Equal(n Money) (bool, error) {
	if m.currency != n.currency {
		return false, ErrCurrencyMismatch
	}
	return m.amount == n.amount, nil
}

func (m Money) LessThan(n Money) (bool, error) {
	if m.currency != n.currency {
		return false, ErrCurrencyMismatch
	}
	return m.amount < n.amount, nil
}

func (m Money) Currency() string {
	return m.currency
}

func (m Money) Amount() int64 {
	return m.amount
}

func (m Money) IsNegative() bool {
	return m.amount < 0
}

func (m Money) IsPositive() bool {
	return m.amount > 0
}

func (m Money) MarshalJSON() ([]byte, error) {
	return json.Marshal(moneyJSON{
		Currency: m.currency,
		Amount:   m.amountString(),
	})
}

func (m *Money) UnmarshalJSON(data []byte) error {
	var raw moneyJSON
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	if err := validateCurrency(raw.Currency); err != nil {
		return err
	}
	amount, err := parseAmountString(raw.Amount)
	if err != nil {
		return err
	}
	m.currency = raw.Currency
	m.amount = amount
	return nil
}

func (m Money) amountString() string {
	s := strconv.FormatInt(m.amount, 10)
	var sign string
	if strings.HasPrefix(s, "-") {
		sign = "-"
		s = s[1:]
	}
	if len(s) < 3 {
		s = strings.Repeat("0", 3-len(s)) + s
	}
	whole := s[:len(s)-2]
	decimal := s[len(s)-2:]
	return sign + whole + "." + decimal
}

func validateCurrency(curr string) error {
	if _, err := currency.ParseISO(curr); err != nil {
		return ErrInvalidCurrency
	}
	return nil
}

var amountRegex = regexp.MustCompile(`^-?(0|[1-9]\d*)\.\d{2}$`)

func parseAmountString(amountStr string) (int64, error) {
	if !amountRegex.MatchString(amountStr) || amountStr == "-0.00" {
		return 0, ErrInvalidAmount
	}
	amountStr = strings.Replace(amountStr, ".", "", 1)
	amount, err := strconv.ParseInt(amountStr, 10, 64)
	if err != nil {
		return 0, ErrOverflow
	}
	return amount, nil
}

type moneyJSON struct {
	Currency string `json:"currency"`
	Amount   string `json:"amount"`
}
