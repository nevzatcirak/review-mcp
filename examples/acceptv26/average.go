package acceptv26

// Average returns the mean amount of the given orders.
func Average(orders []*Order) int {
	if len(orders) == 0 {
		return 0
	}
	sum := 0
	for _, o := range orders {
		sum += o.Amount
	}
	return sum / len(orders)
}
