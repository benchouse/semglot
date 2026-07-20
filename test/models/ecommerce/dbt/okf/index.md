# ecommerce

## Tables

- [fct_orders](/tables/fct_orders.md): Order-grain finance fact. One row per order.
- [fct_order_lines](/tables/fct_order_lines.md): Order-line grain. One row per line item.
- [dim_customer](/tables/dim_customer.md): Customer dimension.
- [dim_product](/tables/dim_product.md): Product dimension.
- [dim_channel](/tables/dim_channel.md): Sales channel dimension.

## Metrics

- [Gross revenue](/metrics/gross_revenue.md): Gross order revenue.
- [Net revenue](/metrics/net_revenue.md): Net booked revenue.
- [Orders](/metrics/orders.md)
- [Refunded orders](/metrics/refunded_orders.md): Count of refunded orders.
- [Average order value](/metrics/aov.md): Average order value (net revenue / orders).
- [Refund rate](/metrics/refund_rate.md): Refunded orders / all orders.
- [Units sold](/metrics/units_sold.md): Units sold.
- [Units per order](/metrics/units_per_order.md): Units per order (cross-table).
