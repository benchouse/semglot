---
type: Table
title: fct_orders
description: Order-grain finance fact. One row per order.
resource: table://ANALYTICS/MAIN/FCT_ORDERS
tags:
  - table
---

# fct_orders

Order-grain finance fact. One row per order.

## Primary key

- `order_id`

## Dimensions

- `order_id` (number): Order surrogate key.
- `customer_sk` (number): Customer the order belongs to.
- `is_refunded` (boolean): Whether the order was refunded.
- `channel_id` (number): Sales channel the order came through.

## Time dimensions

- `order_date` (date): Date the order was placed.

## Measures

- `order_gross_amount` (sum of `order_gross`): Gross order revenue before tax and refunds.
- `order_net_booked_amount` (sum of `order_net_booked`): Net booked revenue (gross minus tax and refunds).
- `orders_count` (count_distinct of `order_id`)
- `refunded_orders_count` (sum of `case when is_refunded then 1 else 0 end`)

## Joins

- [dim_customer](/tables/dim_customer.md): `fct_orders.customer_sk` = `dim_customer.customer_sk`
- [fct_order_lines](/tables/fct_order_lines.md): `fct_order_lines.order_id` = `fct_orders.order_id`
- [dim_channel](/tables/dim_channel.md): `fct_orders.channel_id` = `dim_channel.channel_id`

## Metrics

- [Gross revenue](/metrics/gross_revenue.md)
- [Net revenue](/metrics/net_revenue.md)
- [Orders](/metrics/orders.md)
- [Refunded orders](/metrics/refunded_orders.md)
- [Average order value](/metrics/aov.md)
- [Refund rate](/metrics/refund_rate.md)
