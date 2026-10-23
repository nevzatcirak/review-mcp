package acceptv26

// Order is one order of the shop.
type Order struct {
	ID     string
	Amount int
}

// Store keeps orders in memory.
type Store struct {
	orders map[string]*Order
}

// Find returns the order with the given ID, or nil when it is not known.
func (s *Store) Find(id string) *Order {
	return s.orders[id]
}
