module example.com/orders

go 1.22

require (
	example.com/inventory v0.0.0
	example.com/platform v0.0.0
)

replace (
	example.com/inventory => ../inventory
	example.com/platform => ../platform
)
