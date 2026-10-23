package acceptv26

// Total returns the amount of the order with the given ID.
func Total(s *Store, id string) int {
	o := s.Find(id)
	return o.Amount
}
