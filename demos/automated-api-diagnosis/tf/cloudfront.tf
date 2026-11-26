# HTTPS for everything people and Launchpad send to the demo.
#
# CloudFront answers on its own *.cloudfront.net name and certificate, so the demo needs
# no domain of its own. It reaches the load balancer through a VPC origin: the load
# balancer is internal, and the one hop that is plain HTTP stays inside the VPC.
# Nothing is cached. The UI is an application with a server-sent event stream, not a site.

locals {
  # AWS-managed policies, by id: no caching, and everything the viewer sent goes to the
  # app (cookies, the Authorization header, query strings) along with CloudFront's own
  # headers, among them CloudFront-Forwarded-Proto, which tells the app the viewer used HTTPS.
  cache_policy_caching_disabled   = "4135ea2d-6df8-44a3-9df3-4b5a84be39ad"
  origin_policy_all_viewer_and_cf = "33f36d7e-f396-46d9-90e0-52428a34d9dc"
}

resource "aws_cloudfront_vpc_origin" "demo" {
  vpc_origin_endpoint_config {
    name                   = local.name
    arn                    = aws_lb.demo.arn
    http_port              = 80
    https_port             = 443
    origin_protocol_policy = "http-only"

    origin_ssl_protocols {
      quantity = 1
      items    = ["TLSv1.2"]
    }
  }
}

# CloudFront puts a security group of its own in the VPC when it makes the first VPC
# origin there. The load balancer lets exactly that group in.
data "aws_security_group" "cloudfront_vpc_origin" {
  name   = "CloudFront-VPCOrigins-Service-SG"
  vpc_id = aws_vpc.demo.id

  depends_on = [aws_cloudfront_vpc_origin.demo]
}

resource "aws_cloudfront_distribution" "demo" {
  enabled         = true
  comment         = "Automated API diagnosis ${local.name}"
  is_ipv6_enabled = true
  http_version    = "http2"
  # North America and Europe: the cheapest class, and a demo has one audience.
  price_class = "PriceClass_100"

  origin {
    origin_id   = "alb"
    domain_name = aws_lb.demo.dns_name

    vpc_origin_config {
      vpc_origin_id = aws_cloudfront_vpc_origin.demo.id
      # The longest CloudFront allows by default. The event stream is not bound by it: a
      # response that keeps sending is not idle, and the app pings every 15 seconds.
      origin_read_timeout      = 60
      origin_keepalive_timeout = 60
    }
  }

  default_cache_behavior {
    target_origin_id       = "alb"
    viewer_protocol_policy = "redirect-to-https"
    allowed_methods        = ["GET", "HEAD", "OPTIONS", "PUT", "POST", "PATCH", "DELETE"]
    cached_methods         = ["GET", "HEAD"]
    # The event stream must not be compressed or buffered.
    compress                 = false
    cache_policy_id          = local.cache_policy_caching_disabled
    origin_request_policy_id = local.origin_policy_all_viewer_and_cf
  }

  restrictions {
    geo_restriction {
      restriction_type = "none"
    }
  }

  viewer_certificate {
    cloudfront_default_certificate = true
  }
}
