---
type: Table
title: dim_product
description: Product dimension.
resource: table://ANALYTICS/MAIN/DIM_PRODUCT
tags:
  - table
---

# dim_product

Product dimension.

## Primary key

- `product_id`

## Dimensions

- `product_id` (number): Product surrogate key.
- `category` (varchar): Product category.
- `title` (varchar): Product title.

## Joins

- [fct_order_lines](/tables/fct_order_lines.md): `fct_order_lines.product_id` = `dim_product.product_id`
