---
type: Table
resource: table://ANALYTICS/MAIN/FCT_ORDERS
title: fct_orders
description: Order-grain finance fact. One row per order.
tags:
  - table
timestamp: "2026-07-20T00:00:00+00:00"
---

# Overview

Order-grain finance fact. One row per order.

# Primary key

- `order_id`

# Dimensions

- `order_id` (number): Order surrogate key.
- `customer_sk` (number): Customer the order belongs to.
- `is_refunded` (boolean): Whether the order was refunded.
- `channel_id` (number): Sales channel the order came through.

# Time dimensions

- `order_date` (date): Date the order was placed.

# Measures

- `order_gross_amount` (sum of `order_gross`): Gross order revenue before tax and refunds.
- `order_net_booked_amount` (sum of `order_net_booked`): Net booked revenue (gross minus tax and refunds).
- `orders_count` (count_distinct of `order_id`)
- `refunded_orders_count` (sum of `case when is_refunded then 1 else 0 end`)

# Joins

- [dim_customer](dim_customer.md): `fct_orders.customer_sk` = `dim_customer.customer_sk`
- [fct_order_lines](fct_order_lines.md): `fct_order_lines.order_id` = `fct_orders.order_id`
- [dim_channel](dim_channel.md): `fct_orders.channel_id` = `dim_channel.channel_id`

# Metrics

- [Gross revenue](../metrics/gross_revenue.md): Gross order revenue.
- [Net revenue](../metrics/net_revenue.md): Net booked revenue.
- [Orders](../metrics/orders.md): count(distinct fct_orders.order_id)
- [Refunded orders](../metrics/refunded_orders.md): Count of refunded orders.
- [Average order value](../metrics/aov.md): Average order value (net revenue / orders).
- [Refund rate](../metrics/refund_rate.md): Refunded orders / all orders.
