# Rules

## Key metrics reference

- **Gross revenue**: `sum(fct_orders.order_gross)`. Gross order revenue.
- **Net revenue**: `sum(fct_orders.order_net_booked)`. Net booked revenue.
- **Orders**: `count(distinct fct_orders.order_id)`.
- **Refunded orders**: `sum(case when fct_orders.is_refunded then 1 else 0 end)`. Count of refunded orders.
- **Average order value**: `sum(fct_orders.order_net_booked) / count(distinct fct_orders.order_id)`. Average order value (net revenue / orders).
- **Refund rate**: `sum(case when fct_orders.is_refunded then 1 else 0 end) / count(distinct fct_orders.order_id)`. Refunded orders / all orders.
- **Units sold**: `sum(fct_order_lines.quantity)`. Units sold.
- **Units per order**: `sum(fct_order_lines.quantity) / count(distinct fct_orders.order_id)`. Units per order (cross-table).

## Joins & routing

- `fct_orders.customer_sk → dim_customer.customer_sk`
- `fct_order_lines.order_id → fct_orders.order_id`
- `fct_order_lines.product_id → dim_product.product_id`

## Table traps

- **fct_orders**: Order-grain finance fact. One row per order.
- **fct_order_lines**: Order-line grain. One row per line item.
- **dim_customer**: Customer dimension.
- **dim_product**: Product dimension.
