---
type: Table
resource: table://ANALYTICS/MAIN/DIM_PRODUCT
title: dim_product
description: Product dimension.
tags:
  - table
timestamp: "2026-07-20T00:00:00+00:00"
---

# Overview

Product dimension.

# Primary key

- `product_id`

# Dimensions

- `product_id` (number): Product surrogate key.
- `category` (varchar): Product category.
- `title` (varchar): Product title.

# Joins

- [fct_order_lines](fct_order_lines.md): `fct_order_lines.product_id` = `dim_product.product_id`
