FROM nginx:1.27-alpine

ARG API_BASE=http://localhost:8080
ARG API_UPSTREAM=teach-api:8080

COPY nginx.conf /etc/nginx/conf.d/default.conf
COPY index.html /usr/share/nginx/html/index.html
COPY assets /usr/share/nginx/html/assets
COPY data /usr/share/nginx/html/data

# The API is a separate container. Bake the public path into the static shell
# so a browser does not accidentally call its own localhost.
RUN sed -i "s#http://localhost:8080#${API_BASE}#g" /usr/share/nginx/html/index.html
RUN sed -i "s#__TEACH_API_UPSTREAM__#${API_UPSTREAM}#g" /etc/nginx/conf.d/default.conf

EXPOSE 8080
