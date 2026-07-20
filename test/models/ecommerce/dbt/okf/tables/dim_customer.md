---
type: Table
title: dim_customer
description: Customer dimension.
resource: table://ANALYTICS/MAIN/DIM_CUSTOMER
tags:
  - table
---

# dim_customer

Customer dimension.

## Primary key

- `customer_sk`

## Dimensions

- `customer_sk` (number): Customer surrogate key.
- `customer_segment` (varchar): Marketing segment. Synonyms: segment, customer_type.
- `accepts_marketing` (boolean): Whether the customer opted in to marketing.

## Allowed values

- `customer_segment`: new = First-ever order not yet placed or just placed; returning = Has ordered before; vip = High-value repeat customer; prospect = Signed up, never ordered.

## Joins

- [fct_orders](/tables/fct_orders.md): `fct_orders.customer_sk` = `dim_customer.customer_sk`
