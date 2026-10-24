FROM python:3.12-slim
WORKDIR /app
COPY pyproject.toml ./
COPY src src
COPY testdata testdata
RUN pip install --no-cache-dir . PyYAML
USER 65532:65532
EXPOSE 8080
CMD ["uvicorn", "configurator.app:app", "--host", "0.0.0.0", "--port", "8080"]
