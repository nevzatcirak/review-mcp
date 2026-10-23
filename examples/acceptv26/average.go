package acceptv26

// Average returns the mean amount of the given orders.
func Average(orders []*Order) int {
	sum := 0
	for _, o := range orders {
		sum += o.Amount
	}
	return sum / len(orders)
}
