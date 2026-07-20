---
type: Table
title: fct_order_lines
description: Order-line grain. One row per line item.
resource: table://ANALYTICS/MAIN/FCT_ORDER_LINES
tags:
  - table
---

# fct_order_lines

Order-line grain. One row per line item.

## Primary key

- `order_line_id`

## Dimensions

- `order_line_id` (number): Line-item surrogate key.
- `order_id` (number): Order the line belongs to.
- `product_id` (number): Product sold on the line.

## Time dimensions

- `order_date` (date): Date the parent order was placed.

## Measures

- `quantity` (sum): Units sold on the line.
- `net_line_revenue` (sum): Net revenue for the line.

## Joins

- [fct_orders](/tables/fct_orders.md): `fct_order_lines.order_id` = `fct_orders.order_id`
- [dim_product](/tables/dim_product.md): `fct_order_lines.product_id` = `dim_product.product_id`

## Metrics

- [Units sold](/metrics/units_sold.md)
- [Units per order](/metrics/units_per_order.md)
