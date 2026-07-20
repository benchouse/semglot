---
type: Table
resource: table://ANALYTICS/MAIN/DIM_CUSTOMER
title: dim_customer
description: Customer dimension.
tags:
  - table
timestamp: "2026-07-20T00:00:00+00:00"
---

# Overview

Customer dimension.

# Primary key

- `customer_sk`

# Dimensions

- `customer_sk` (number): Customer surrogate key.
- `customer_segment` (varchar): Marketing segment. Synonyms: segment, customer_type.
- `accepts_marketing` (boolean): Whether the customer opted in to marketing.

# Allowed values

- `customer_segment`: new = First-ever order not yet placed or just placed; returning = Has ordered before; vip = High-value repeat customer; prospect = Signed up, never ordered.

# Joins

- [fct_orders](fct_orders.md): `fct_orders.customer_sk` = `dim_customer.customer_sk`
