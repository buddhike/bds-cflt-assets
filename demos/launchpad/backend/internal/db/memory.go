package db

import (
	"context"
	"fmt"
	"maps"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

// Memory is a DB that keeps its items in memory, for tests. It follows
// DynamoDB's rules for the operations Launchpad uses: conditions are checked
// against the stored item before a write, and Update creates a missing item.
type Memory struct {
	mu    sync.Mutex
	items map[[2]string]Item
}

// NewMemory returns an empty Memory.
func NewMemory() *Memory { return &Memory{items: map[[2]string]Item{}} }

func (m *Memory) Get(_ context.Context, pk, sk string) (Item, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if item, ok := m.items[[2]string{pk, sk}]; ok {
		return maps.Clone(item), nil
	}
	return nil, nil
}

func (m *Memory) Put(_ context.Context, item Item, cond Cond) error {
	pk, sk, err := keyOf(item)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if ok, err := holds(cond, m.items[[2]string{pk, sk}]); err != nil {
		return err
	} else if !ok {
		return ErrConditionFailed
	}
	m.items[[2]string{pk, sk}] = maps.Clone(item)
	return nil
}

func (m *Memory) Update(_ context.Context, pk, sk string, u Update, cond Cond) (Item, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	current := m.items[[2]string{pk, sk}]
	if ok, err := holds(cond, current); err != nil {
		return nil, err
	} else if !ok {
		return nil, ErrConditionFailed
	}
	next := maps.Clone(current)
	if next == nil {
		next = Key(pk, sk)
	}
	for name, v := range u.Add {
		old, ok := next[name].(*types.AttributeValueMemberN)
		if !ok {
			return nil, fmt.Errorf("memory: cannot add to %q, which is not a number", name)
		}
		n, err := strconv.ParseInt(old.Value, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("memory: %q is not a whole number: %w", name, err)
		}
		next[name] = &types.AttributeValueMemberN{Value: strconv.FormatInt(n+v, 10)}
	}
	for name, v := range u.Set {
		av, err := attributevalue.Marshal(v)
		if err != nil {
			return nil, fmt.Errorf("memory: marshal %q: %w", name, err)
		}
		next[name] = av
	}
	m.items[[2]string{pk, sk}] = next
	return maps.Clone(next), nil
}

func (m *Memory) Delete(_ context.Context, pk, sk string, cond Cond) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if ok, err := holds(cond, m.items[[2]string{pk, sk}]); err != nil {
		return err
	} else if !ok {
		return ErrConditionFailed
	}
	delete(m.items, [2]string{pk, sk})
	return nil
}

func (m *Memory) Query(_ context.Context, pk, skPrefix string) ([]Item, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var keys [][2]string
	for k := range m.items {
		if k[0] == pk && strings.HasPrefix(k[1], skPrefix) {
			keys = append(keys, k)
		}
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i][1] < keys[j][1] })
	out := make([]Item, len(keys))
	for i, k := range keys {
		out[i] = maps.Clone(m.items[k])
	}
	return out, nil
}

func keyOf(item Item) (pk, sk string, err error) {
	p, ok1 := item["pk"].(*types.AttributeValueMemberS)
	s, ok2 := item["sk"].(*types.AttributeValueMemberS)
	if !ok1 || !ok2 {
		return "", "", fmt.Errorf("memory: item has no pk and sk")
	}
	return p.Value, s.Value, nil
}

// holds evaluates cond against item. A nil item has no attributes.
func holds(cond Cond, item Item) (bool, error) {
	if cond == nil {
		return true, nil
	}
	switch c := cond.(type) {
	case absent:
		_, present := item[c.attr]
		return !present, nil
	case equal:
		return attrEquals(item, c.attr, c.value)
	case less:
		have, ok := item[c.attr].(*types.AttributeValueMemberN)
		if !ok {
			return false, nil
		}
		want, err := canonical(c.value)
		if err != nil {
			return false, err
		}
		limit, ok := want.(float64)
		if !ok {
			return false, fmt.Errorf("memory: cannot compare %q with %v", c.attr, c.value)
		}
		n, err := strconv.ParseFloat(have.Value, 64)
		if err != nil {
			return false, err
		}
		return n < limit, nil
	case oneOf:
		for _, v := range c.values {
			if ok, err := attrEquals(item, c.attr, v); err != nil || ok {
				return ok, err
			}
		}
		return false, nil
	case and:
		for _, sub := range c {
			if ok, err := holds(sub, item); err != nil || !ok {
				return false, err
			}
		}
		return true, nil
	case or:
		for _, sub := range c {
			if ok, err := holds(sub, item); err != nil || ok {
				return ok, err
			}
		}
		return false, nil
	}
	return false, fmt.Errorf("memory: unknown condition %T", cond)
}

func attrEquals(item Item, attr string, value any) (bool, error) {
	have, ok := item[attr]
	if !ok {
		return false, nil
	}
	var haveValue any
	if err := attributevalue.Unmarshal(have, &haveValue); err != nil {
		return false, err
	}
	want, err := canonical(value)
	if err != nil {
		return false, err
	}
	return reflect.DeepEqual(haveValue, want), nil
}

// canonical turns a Go value into what attributevalue.Unmarshal into an any
// would give for it, so stored and given values compare.
func canonical(v any) (any, error) {
	av, err := attributevalue.Marshal(v)
	if err != nil {
		return nil, err
	}
	var out any
	if err := attributevalue.Unmarshal(av, &out); err != nil {
		return nil, err
	}
	return out, nil
}
