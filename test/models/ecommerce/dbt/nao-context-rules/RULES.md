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

## Columns

- `fct_orders.order_id`: Order surrogate key.
- `fct_orders.customer_sk`: Customer the order belongs to.
- `fct_orders.is_refunded`: Whether the order was refunded.
- `fct_orders.channel_id`: Sales channel the order came through.
- `fct_orders.aov`: Precomputed average order value; superseded by the computed aov metric.
- `fct_orders.order_date`: Date the order was placed.
- `fct_order_lines.order_line_id`: Line-item surrogate key.
- `fct_order_lines.order_id`: Order the line belongs to.
- `fct_order_lines.product_id`: Product sold on the line.
- `fct_order_lines.order_date`: Date the parent order was placed.
- `dim_customer.customer_sk`: Customer surrogate key.
- `dim_customer.customer_segment`: Marketing segment. Synonyms: segment, customer_type.
- `dim_customer.accepts_marketing`: Whether the customer opted in to marketing.
- `dim_product.product_id`: Product surrogate key.
- `dim_product.category`: Product category.
- `dim_product.title`: Product title.
- `obt_sales.order_line_id`: Line-item surrogate key.
- `obt_sales.order_id`: Order the line belongs to.
- `obt_sales.customer_segment`: Marketing segment.
- `obt_sales.is_refunded`: Whether the order line was refunded.
- `obt_sales.order_date`: Date the order was placed.
- `dim_channel.channel_id`: Channel surrogate key.
- `dim_channel.channel_name`: Channel display name.

## Allowed values

- `dim_customer.customer_segment`: new = First-ever order not yet placed or just placed; returning = Has ordered before; vip = High-value repeat customer; prospect = Signed up, never ordered.

## Joins & routing

- `fct_orders.customer_sk → dim_customer.customer_sk`
- `fct_order_lines.order_id → fct_orders.order_id`
- `fct_order_lines.product_id → dim_product.product_id`
- `obt_sales.order_id → fct_orders.order_id`
- `fct_orders.channel_id → dim_channel.channel_id`

## Table reference

- **fct_orders**: Order-grain finance fact. One row per order.
- **fct_order_lines**: Order-line grain. One row per line item.
- **dim_customer**: Customer dimension.
- **dim_product**: Product dimension.
- **obt_sales**: Wide sales table at order-line grain. One row per order line. Measures only, no metrics.
- **dim_channel**: Sales channel dimension.
