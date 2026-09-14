package money

import (
	"encoding/json"
	"fmt"
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNew(t *testing.T) {
	t.Run("invalid currency", func(t *testing.T) {
		m, err := New("invalid currency", "0.00")
		assert.ErrorIs(t, err, ErrInvalidCurrency)
		assert.Zero(t, m)
	})
	t.Run("not a number", func(t *testing.T) {
		m, err := New("BRL", "NaN")
		assert.ErrorIs(t, err, ErrInvalidAmount)
		assert.Zero(t, m)
	})

	t.Run("scientific notation", func(t *testing.T) {
		m, err := New("BRL", "1e10")
		assert.ErrorIs(t, err, ErrInvalidAmount)
		assert.Zero(t, m)
	})

	t.Run("scientific notation", func(t *testing.T) {
		m, err := New("BRL", "Infinity")
		assert.ErrorIs(t, err, ErrInvalidAmount)
		assert.Zero(t, m)
	})

	t.Run("no decimal", func(t *testing.T) {
		m, err := New("BRL", "1")
		assert.ErrorIs(t, err, ErrInvalidAmount)
		assert.Zero(t, m)
	})

	t.Run("1 decimal", func(t *testing.T) {
		m, err := New("BRL", "1.0")
		assert.ErrorIs(t, err, ErrInvalidAmount)
		assert.Zero(t, m)
	})

	t.Run("3 decimals", func(t *testing.T) {
		m, err := New("BRL", "1.000")
		assert.ErrorIs(t, err, ErrInvalidAmount)
		assert.Zero(t, m)
	})

	t.Run("leading zeroes", func(t *testing.T) {
		m, err := New("BRL", "01.00")
		assert.ErrorIs(t, err, ErrInvalidAmount)
		assert.Zero(t, m)

		m, err = New("BRL", "-01.00")
		assert.ErrorIs(t, err, ErrInvalidAmount)
		assert.Zero(t, m)
	})

	t.Run("+ sign", func(t *testing.T) {
		m, err := New("BRL", "+1.00")
		assert.ErrorIs(t, err, ErrInvalidAmount)
		assert.Zero(t, m)
	})

	t.Run("maximum", func(t *testing.T) {
		max := "92233720368547758.07"
		m, err := New("BRL", max)
		assert.NoError(t, err)
		assert.Equal(t, Money{"BRL", math.MaxInt64}, m)
	})

	t.Run("overflow", func(t *testing.T) {
		maxPlusOne := "92233720368547758.08" // max + R$0.01
		m, err := New("BRL", maxPlusOne)
		assert.ErrorIs(t, err, ErrOverflow)
		assert.Zero(t, m)
	})

	t.Run("minimum", func(t *testing.T) {
		min := "-92233720368547758.08"
		m, err := New("BRL", min)
		assert.NoError(t, err)
		assert.Equal(t, Money{"BRL", math.MinInt64}, m)
	})

	t.Run("underflow", func(t *testing.T) {
		minMinusOne := "-92233720368547758.09" // = min - R$0.01
		m, err := New("BRL", minMinusOne)
		assert.ErrorIs(t, err, ErrOverflow)
		assert.Zero(t, m)
	})

	t.Run("positive", func(t *testing.T) {
		m, err := New("BRL", "123.45")
		assert.NoError(t, err)
		assert.Equal(t, Money{"BRL", 12345}, m)
	})

	t.Run("least positive", func(t *testing.T) {
		m, err := New("BRL", "0.01")
		assert.NoError(t, err)
		assert.Equal(t, Money{"BRL", 1}, m)
	})

	t.Run("zero", func(t *testing.T) {
		m, err := New("BRL", "0.00")
		assert.NoError(t, err)
		assert.Equal(t, Money{"BRL", 0}, m)
	})

	t.Run("negative zero", func(t *testing.T) {
		m, err := New("BRL", "-0.00")
		assert.ErrorIs(t, err, ErrInvalidAmount)
		assert.Zero(t, m)
	})

	t.Run("greatest negative", func(t *testing.T) {
		m, err := New("BRL", "-0.01")
		assert.NoError(t, err)
		assert.Equal(t, Money{"BRL", -1}, m)
	})

	t.Run("negative", func(t *testing.T) {
		m, err := New("BRL", "-123.45")
		assert.NoError(t, err)
		assert.Equal(t, Money{"BRL", -12345}, m)
	})
}

func TestFromMinorUnits(t *testing.T) {
	t.Run("invalid currency", func(t *testing.T) {
		m, err := FromMinorUnits("invalid currency", 12345)
		assert.ErrorIs(t, err, ErrInvalidCurrency)
		assert.Zero(t, m)
	})

	t.Run("positive", func(t *testing.T) {
		m, err := FromMinorUnits("BRL", 12345)
		assert.NoError(t, err)
		assert.Equal(t, Money{"BRL", 12345}, m)
	})

	t.Run("zero", func(t *testing.T) {
		m, err := FromMinorUnits("BRL", 0)
		assert.NoError(t, err)
		assert.Equal(t, Money{"BRL", 0}, m)
	})

	t.Run("negative", func(t *testing.T) {
		m, err := FromMinorUnits("BRL", -12345)
		assert.NoError(t, err)
		assert.Equal(t, Money{"BRL", -12345}, m)
	})
}

func TestZero(t *testing.T) {
	t.Run("invalid currency", func(t *testing.T) {
		m, err := Zero("invalid currency")
		assert.ErrorIs(t, err, ErrInvalidCurrency)
		assert.Zero(t, m)
	})

	m, err := Zero("BRL")
	assert.NoError(t, err)
	assert.Equal(t, Money{"BRL", 0}, m)
}

func TestMoneyNegate(t *testing.T) {
	t.Run("amount is too small", func(t *testing.T) {
		m := Money{"BRL", math.MinInt64}
		n, err := m.Negate()
		assert.ErrorIs(t, err, ErrOverflow)
		assert.Zero(t, n)
	})

	t.Run("into negative", func(t *testing.T) {
		m := Money{"BRL", 12345}
		n, err := m.Negate()
		assert.NoError(t, err)
		assert.Equal(t, Money{"BRL", -12345}, n)
	})

	t.Run("into positive", func(t *testing.T) {
		m := Money{"BRL", -12345}
		n, err := m.Negate()
		assert.NoError(t, err)
		assert.Equal(t, Money{"BRL", 12345}, n)
	})
}

func TestMoneyAdd(t *testing.T) {
	t.Run("currency mismatch", func(t *testing.T) {
		a := Money{"BRL", 0}
		b := Money{"USD", 0}
		c, err := a.Add(b)
		assert.ErrorIs(t, err, ErrCurrencyMismatch)
		assert.Zero(t, c)
	})

	t.Run("positive", func(t *testing.T) {
		a := Money{"BRL", 100}
		b := Money{"BRL", 200}
		c, err := a.Add(b)
		assert.NoError(t, err)
		assert.Equal(t, Money{"BRL", 300}, c)
	})

	t.Run("maximum", func(t *testing.T) {
		a := Money{"BRL", math.MaxInt64 - 1}
		b := Money{"BRL", 1}
		c, err := a.Add(b)
		assert.NoError(t, err)
		assert.Equal(t, Money{"BRL", math.MaxInt64}, c)
	})

	t.Run("overflow", func(t *testing.T) {
		a := Money{"BRL", math.MaxInt64}
		b := Money{"BRL", 1}
		c, err := a.Add(b)
		assert.ErrorIs(t, err, ErrOverflow)
		assert.Zero(t, c)
	})

	t.Run("negative", func(t *testing.T) {
		a := Money{"BRL", -100}
		b := Money{"BRL", -200}
		c, err := a.Add(b)
		assert.NoError(t, err)
		assert.Equal(t, Money{"BRL", -300}, c)
	})

	t.Run("minimum", func(t *testing.T) {
		a := Money{"BRL", math.MinInt64 + 1}
		b := Money{"BRL", -1}
		c, err := a.Add(b)
		assert.NoError(t, err)
		assert.Equal(t, Money{"BRL", math.MinInt64}, c)
	})

	t.Run("underflow", func(t *testing.T) {
		a := Money{"BRL", math.MinInt64}
		b := Money{"BRL", -1}
		c, err := a.Add(b)
		assert.ErrorIs(t, err, ErrOverflow)
		assert.Zero(t, c)
	})
}

func TestMoneySubtract(t *testing.T) {
	t.Run("currency mismatch", func(t *testing.T) {
		a := Money{"BRL", 0}
		b := Money{"USD", 0}
		c, err := a.Subtract(b)
		assert.ErrorIs(t, err, ErrCurrencyMismatch)
		assert.Zero(t, c)
	})

	t.Run("negative", func(t *testing.T) {
		a := Money{"BRL", 100}
		b := Money{"BRL", 200}
		c, err := a.Subtract(b)
		assert.NoError(t, err)
		assert.Equal(t, Money{"BRL", -100}, c)
	})

	t.Run("minimum", func(t *testing.T) {
		a := Money{"BRL", math.MinInt64}
		b := Money{"BRL", 0}
		c, err := a.Subtract(b)
		assert.NoError(t, err)
		assert.Equal(t, Money{"BRL", math.MinInt64}, c)
	})

	t.Run("too small", func(t *testing.T) {
		a := Money{"BRL", 0}
		b := Money{"BRL", math.MinInt64}
		c, err := a.Subtract(b)
		assert.ErrorIs(t, err, ErrOverflow)
		assert.Zero(t, c)
	})

	t.Run("underflow", func(t *testing.T) {
		a := Money{"BRL", math.MinInt64}
		b := Money{"BRL", 1}
		c, err := a.Subtract(b)
		assert.ErrorIs(t, err, ErrOverflow)
		assert.Zero(t, c)
	})

	t.Run("positive", func(t *testing.T) {
		a := Money{"BRL", 200}
		b := Money{"BRL", 100}
		c, err := a.Subtract(b)
		assert.NoError(t, err)
		assert.Equal(t, Money{"BRL", 100}, c)
	})

	t.Run("maximum", func(t *testing.T) {
		a := Money{"BRL", math.MaxInt64}
		b := Money{"BRL", 0}
		c, err := a.Subtract(b)
		assert.NoError(t, err)
		assert.Equal(t, Money{"BRL", math.MaxInt64}, c)
	})

	t.Run("overflow", func(t *testing.T) {
		a := Money{"BRL", math.MaxInt64}
		b := Money{"BRL", -1}
		c, err := a.Subtract(b)
		assert.ErrorIs(t, err, ErrOverflow)
		assert.Zero(t, c)
	})
}

func TestMoneyEqual(t *testing.T) {
	t.Run("currency mismatch", func(t *testing.T) {
		a := Money{"BRL", 100}
		b := Money{"USD", 100}
		ok, err := a.Equal(b)
		assert.ErrorIs(t, err, ErrCurrencyMismatch)
		assert.Zero(t, ok)
	})

	t.Run("different", func(t *testing.T) {
		a := Money{"BRL", 100}
		b := Money{"BRL", 200}
		ok, err := a.Equal(b)
		assert.NoError(t, err)
		assert.False(t, ok)
	})

	t.Run("equal", func(t *testing.T) {
		a := Money{"BRL", 100}
		b := Money{"BRL", 100}
		ok, err := a.Equal(b)
		assert.NoError(t, err)
		assert.True(t, ok)
	})
}

func TestLessThan(t *testing.T) {
	t.Run("currency mismatch", func(t *testing.T) {
		a := Money{"BRL", 100}
		b := Money{"USD", 200}
		ok, err := a.LessThan(b)
		assert.ErrorIs(t, err, ErrCurrencyMismatch)
		assert.Zero(t, ok)
	})

	t.Run("greater", func(t *testing.T) {
		a := Money{"BRL", 200}
		b := Money{"BRL", 100}
		ok, err := a.LessThan(b)
		assert.NoError(t, err)
		assert.False(t, ok)
	})

	t.Run("equal", func(t *testing.T) {
		a := Money{"BRL", 100}
		b := Money{"BRL", 100}
		ok, err := a.LessThan(b)
		assert.NoError(t, err)
		assert.False(t, ok)
	})

	t.Run("less", func(t *testing.T) {
		a := Money{"BRL", 100}
		b := Money{"BRL", 200}
		ok, err := a.LessThan(b)
		assert.NoError(t, err)
		assert.True(t, ok)
	})
}

func TestMoneyCurrency(t *testing.T) {
	m := Money{"BRL", 12345}
	assert.Equal(t, "BRL", m.Currency())
}

func TestMoneyAmount(t *testing.T) {
	m := Money{"BRL", 12345}
	assert.Equal(t, int64(12345), m.Amount())
}

func TestMoneyIsNegative(t *testing.T) {
	t.Run("positive", func(t *testing.T) {
		m := Money{"BRL", 1}
		assert.False(t, m.IsNegative())
	})

	t.Run("zero", func(t *testing.T) {
		m := Money{"BRL", 0}
		assert.False(t, m.IsNegative())
	})

	t.Run("negative", func(t *testing.T) {
		m := Money{"BRL", -1}
		assert.True(t, m.IsNegative())
	})
}

func TestMoneyIsPositive(t *testing.T) {
	t.Run("positive", func(t *testing.T) {
		m := Money{"BRL", 1}
		assert.True(t, m.IsPositive())
	})

	t.Run("zero", func(t *testing.T) {
		m := Money{"BRL", 0}
		assert.False(t, m.IsPositive())
	})

	t.Run("negative", func(t *testing.T) {
		m := Money{"BRL", -1}
		assert.False(t, m.IsPositive())
	})
}

func TestMoneyMarshalJSON(t *testing.T) {
	t.Run("zero", func(t *testing.T) {
		m := Money{"BRL", 0}
		b, err := json.Marshal(m)
		assert.NoError(t, err)
		assert.JSONEq(t, `{"currency": "BRL", "amount": "0.00"}`, string(b))
	})

	t.Run("R$0.01", func(t *testing.T) {
		m := Money{"BRL", 1}
		b, err := json.Marshal(m)
		assert.NoError(t, err)
		assert.JSONEq(t, `{"currency": "BRL", "amount": "0.01"}`, string(b))
	})

	t.Run("R$0.10", func(t *testing.T) {
		m := Money{"BRL", 10}
		b, err := json.Marshal(m)
		assert.NoError(t, err)
		assert.JSONEq(t, `{"currency": "BRL", "amount": "0.10"}`, string(b))
	})

	t.Run("R$1.00", func(t *testing.T) {
		m := Money{"BRL", 100}
		b, err := json.Marshal(m)
		assert.NoError(t, err)
		assert.JSONEq(t, `{"currency": "BRL", "amount": "1.00"}`, string(b))
	})

	t.Run("R$123.45", func(t *testing.T) {
		m := Money{"BRL", 12345}
		b, err := json.Marshal(m)
		assert.NoError(t, err)
		assert.JSONEq(t, `{"currency": "BRL", "amount": "123.45"}`, string(b))
	})

	t.Run("-R$0.01", func(t *testing.T) {
		m := Money{"BRL", -1}
		b, err := json.Marshal(m)
		assert.NoError(t, err)
		assert.JSONEq(t, `{"currency": "BRL", "amount": "-0.01"}`, string(b))
	})

	t.Run("-R$123.45", func(t *testing.T) {
		m := Money{"BRL", -12345}
		b, err := json.Marshal(m)
		assert.NoError(t, err)
		assert.JSONEq(t, `{"currency": "BRL", "amount": "-123.45"}`, string(b))
	})
}

func TestMoneyUnmarshalJSON(t *testing.T) {
	t.Run("malformed json", func(t *testing.T) {
		b := []byte(`{"currency": "BRL", "amount": 123.45}`) // invalid: amount should be a string
		var m Money
		err := json.Unmarshal(b, &m)
		assert.Error(t, err)
		assert.Zero(t, m)
	})

	t.Run("invalid currency", func(t *testing.T) {
		b := []byte(`{"currency": "invalid currency", "amount":"123.45"}`)
		var m Money
		err := json.Unmarshal(b, &m)
		assert.ErrorIs(t, err, ErrInvalidCurrency)
		assert.Zero(t, m)
	})

	t.Run("not a number", func(t *testing.T) {
		b := []byte(`{"currency": "BRL", "amount":"NaN"}`)
		var m Money
		err := json.Unmarshal(b, &m)
		assert.ErrorIs(t, err, ErrInvalidAmount)
		assert.Zero(t, m)
	})

	t.Run("scientific notation", func(t *testing.T) {
		b := []byte(`{"currency": "BRL", "amount":"1e10"}`)
		var m Money
		err := json.Unmarshal(b, &m)
		assert.ErrorIs(t, err, ErrInvalidAmount)
		assert.Zero(t, m)
	})

	t.Run("infinity", func(t *testing.T) {
		b := []byte(`{"currency": "BRL", "amount":"Infinity"}`)
		var m Money
		err := json.Unmarshal(b, &m)
		assert.ErrorIs(t, err, ErrInvalidAmount)
		assert.Zero(t, m)
	})

	t.Run("no decimal", func(t *testing.T) {
		b := []byte(`{"currency": "BRL", "amount":"123"}`)
		var m Money
		err := json.Unmarshal(b, &m)
		assert.ErrorIs(t, err, ErrInvalidAmount)
		assert.Zero(t, m)
	})

	t.Run("1 decimal", func(t *testing.T) {
		b := []byte(`{"currency": "BRL", "amount":"123.4"}`)
		var m Money
		err := json.Unmarshal(b, &m)
		assert.ErrorIs(t, err, ErrInvalidAmount)
		assert.Zero(t, m)
	})

	t.Run("3 decimals", func(t *testing.T) {
		b := []byte(`{"currency": "BRL", "amount":"123.456"}`)
		var m Money
		err := json.Unmarshal(b, &m)
		assert.ErrorIs(t, err, ErrInvalidAmount)
		assert.Zero(t, m)
	})

	t.Run("leading zeroes", func(t *testing.T) {
		b := []byte(`{"currency": "BRL", "amount":"0123.45"}`)
		var m Money
		err := json.Unmarshal(b, &m)
		assert.ErrorIs(t, err, ErrInvalidAmount)
		assert.Zero(t, m)

		b = []byte(`{"currency": "BRL", "amount":"-0123.45"}`)
		err = json.Unmarshal(b, &m)
		assert.ErrorIs(t, err, ErrInvalidAmount)
		assert.Zero(t, m)
	})

	t.Run("+ sign", func(t *testing.T) {
		b := []byte(`{"currency": "BRL", "amount":"+123.45"}`)
		var m Money
		err := json.Unmarshal(b, &m)
		assert.ErrorIs(t, err, ErrInvalidAmount)
		assert.Zero(t, m)
	})
	t.Run("maximum", func(t *testing.T) {
		max := "92233720368547758.07" // equivalent to math.MaxInt64
		s := fmt.Sprintf(`{"currency":"BRL","amount":%q}`, max)
		var m Money
		err := json.Unmarshal([]byte(s), &m)
		assert.NoError(t, err)
		assert.Equal(t, Money{"BRL", math.MaxInt64}, m)
	})

	t.Run("overflow", func(t *testing.T) {
		maxPlusOne := "92233720368547758.08" // max + R$0.01
		s := fmt.Sprintf(`{"currency":"BRL","amount":%q}`, maxPlusOne)
		var m Money
		err := json.Unmarshal([]byte(s), &m)
		assert.ErrorIs(t, err, ErrOverflow)
		assert.Zero(t, m)
	})

	t.Run("minimum", func(t *testing.T) {
		min := "-92233720368547758.08" // equivalent to math.MinInt64
		s := fmt.Sprintf(`{"currency":"BRL","amount":%q}`, min)
		var m Money
		err := json.Unmarshal([]byte(s), &m)
		assert.NoError(t, err)
		assert.Equal(t, Money{"BRL", math.MinInt64}, m)
	})

	t.Run("underflow", func(t *testing.T) {
		minMinusOne := "-92233720368547758.09" // max - R$0.01
		s := fmt.Sprintf(`{"currency":"BRL","amount":%q}`, minMinusOne)
		var m Money
		err := json.Unmarshal([]byte(s), &m)
		assert.ErrorIs(t, err, ErrOverflow)
		assert.Zero(t, m)
	})

	t.Run("positive", func(t *testing.T) {
		b := []byte(`{"currency": "BRL", "amount":"123.45"}`) // (math.MinInt64 - 1) / 100
		var m Money
		err := json.Unmarshal(b, &m)
		assert.NoError(t, err)
		assert.Equal(t, Money{"BRL", 12345}, m)
	})

	t.Run("zero", func(t *testing.T) {
		b := []byte(`{"currency": "BRL", "amount":"0.00"}`) // (math.MinInt64 - 1) / 100
		var m Money
		err := json.Unmarshal(b, &m)
		assert.NoError(t, err)
		assert.Equal(t, Money{"BRL", 0}, m)
	})

	t.Run("negative", func(t *testing.T) {
		b := []byte(`{"currency": "BRL", "amount":"-123.45"}`) // (math.MinInt64 - 1) / 100
		var m Money
		err := json.Unmarshal(b, &m)
		assert.NoError(t, err)
		assert.Equal(t, Money{"BRL", -12345}, m)
	})
}
